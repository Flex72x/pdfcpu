/*
Copyright 2026 The pdfcpu Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0
*/

package pdfcpu

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image/jpeg"
	"io"

	"github.com/pdfcpu/pdfcpu/internal/contextutil"
	"github.com/pdfcpu/pdfcpu/pkg/filter"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/safemath"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

var (
	ErrInvalidImageSource     = errors.New("pdfcpu: invalid image source")
	ErrUnsupportedImageSource = errors.New("pdfcpu: unsupported image source")
	ErrImageSourceLimit       = errors.New("pdfcpu: image source resource limit exceeded")
)

// ImageSourceKind identifies the representation of an image's original samples.
type ImageSourceKind uint8

const (
	ImageSourceRaster ImageSourceKind = iota
	ImageSourceJPEG
	ImageSourceJP2
	ImageSourceJ2K
	ImageSourceJBIG2
)

// ImageSource is independent of page rendering: Decode, Mask, SMask, Matte and
// external color profile transforms are never applied. Data and Palette borrow
// immutable PDF buffers where possible; keep them unchanged until use ends.
// Raster Data is MSB-first, row-aligned, with big-endian 16-bit samples. Palette
// entries are interleaved components of ColorSpace; raster components are indexes
// when Palette is nonempty. Globals is the decoded JBIG2Globals dependency.
type ImageSource struct {
	Kind                                                   ImageSourceKind
	Data                                                   []byte
	Width, Height, BitsPerComponent, Components, RowStride int
	ColorSpace                                             string
	Palette                                                []byte
	Globals                                                []byte
}

// ExtractEncodedImageSource removes only filters before a terminal DCT, JPX or
// JBIG2 filter. In particular it does not decode JPEG samples or require PDF
// ColorSpace to interpret a standalone JPEG. handled is false for raster streams.
func ExtractEncodedImageSource(opCtx context.Context, doc *model.Context, sd *types.StreamDict) (source *ImageSource, handled bool, err error) {
	if err = contextutil.Check(opCtx); err != nil {
		return nil, false, err
	}
	if err = requireContextWithXRefTable(doc); err != nil {
		return nil, false, err
	}
	if err = requireStreamDict(sd); err != nil {
		return nil, false, err
	}
	n := len(sd.FilterPipeline)
	if n == 0 {
		return nil, false, nil
	}
	terminal := sd.FilterPipeline[n-1].Name
	if terminal != filter.DCT && terminal != filter.JPX && terminal != filter.JBIG2 {
		return nil, false, nil
	}
	if sd.RawLength() > imageLimits(doc.XRefTable).MaxStreamBytes {
		return nil, true, ErrImageSourceLimit
	}
	source = &ImageSource{}
	if n == 1 {
		source.Data, err = sd.RawBytes(imageLimits(doc.XRefTable).MaxStreamBytes)
		if err != nil {
			return nil, true, imageSourceDecodeError(err)
		}
	}
	if n > 1 {
		local := *sd
		local.Dict = sd.Dict.Clone().(types.Dict)
		local.Content = nil
		local.FilterPipeline = append([]types.PDFFilter(nil), sd.FilterPipeline[:n-1]...)
		source.Data, err = local.DecodeLengthWithLimit(-1, imageLimits(doc.XRefTable).MaxDecodeBytes)
		if err != nil {
			return nil, true, imageSourceDecodeError(err)
		}
	}
	if err = contextutil.Check(opCtx); err != nil {
		return nil, true, err
	}
	switch terminal {
	case filter.DCT:
		source.Kind = ImageSourceJPEG
	case filter.JPX:
		switch {
		case len(source.Data) >= 12 && bytes.Equal(source.Data[:12], []byte{0, 0, 0, 12, 'j', 'P', ' ', ' ', 13, 10, 135, 10}):
			source.Kind = ImageSourceJP2
		case len(source.Data) >= 4 && bytes.Equal(source.Data[:4], []byte{255, 79, 255, 81}):
			source.Kind = ImageSourceJ2K
		default:
			return nil, true, fmt.Errorf("%w: JPX signature", ErrInvalidImageSource)
		}
	case filter.JBIG2:
		source.Kind = ImageSourceJBIG2
		source.Width, err = imageWidth(doc, sd, 0)
		if err == nil {
			source.Height, err = imageHeight(doc, sd, 0)
		}
		if err != nil {
			return nil, true, fmt.Errorf("%w: %w", ErrInvalidImageSource, err)
		}
		if err = sourceRasterLimits(doc, source.Width, source.Height, 1, 1); err != nil {
			return nil, true, err
		}
		source.Components, source.BitsPerComponent, source.ColorSpace = 1, 1, model.DeviceGrayCS
		if parms := sd.FilterPipeline[n-1].DecodeParms; parms != nil {
			if o, ok := parms.Find("JBIG2Globals"); ok {
				globals, e := dereferenceRequiredStreamDict(doc.XRefTable, o, "JBIG2Globals")
				if e != nil {
					return nil, true, fmt.Errorf("%w: %w", ErrInvalidImageSource, e)
				}
				local := *globals
				local.Dict = globals.Dict.Clone().(types.Dict)
				local.Content = nil
				source.Globals, e = local.DecodeLengthWithLimit(-1, imageLimits(doc.XRefTable).MaxDecodeBytes)
				if e != nil {
					return nil, true, imageSourceDecodeError(e)
				}
			}
		}
	}
	if int64(len(source.Data)) > imageLimits(doc.XRefTable).MaxDecodeBytes || int64(len(source.Globals)) > imageLimits(doc.XRefTable).MaxDecodeBytes {
		return nil, true, ErrImageSourceLimit
	}
	if err = contextutil.Check(opCtx); err != nil {
		return nil, true, err
	}
	return source, true, nil
}

func imageSourceDecodeError(err error) error {
	if errors.Is(err, filter.ErrDecodeLimitExceeded) {
		return fmt.Errorf("%w: %w", ErrImageSourceLimit, err)
	}
	if errors.Is(err, filter.ErrUnsupportedFilter) {
		return fmt.Errorf("%w: %w", ErrUnsupportedImageSource, err)
	}
	return fmt.Errorf("%w: filter decode: %w", ErrInvalidImageSource, err)
}

func sourceRasterLimits(doc *model.Context, w, h, components, bpc int) error {
	if w <= 0 || h <= 0 || components <= 0 || (bpc != 1 && bpc != 2 && bpc != 4 && bpc != 8 && bpc != 16) {
		return fmt.Errorf("%w: raster layout %dx%dx%d/%d", ErrUnsupportedImageSource, w, h, components, bpc)
	}
	limits := imageLimits(doc.XRefTable)
	pixels, err := safemath.MultiplyInt64(int64(w), int64(h))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrImageSourceLimit, err)
	}
	if pixels > limits.MaxImagePixels {
		return ErrImageSourceLimit
	}
	rawBytes, err := checkedImageBytes(w, h, components, bpc)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrImageSourceLimit, err)
	}
	if rawBytes > limits.MaxImageBytes {
		return ErrImageSourceLimit
	}
	return nil
}

// ExtractImageSource obtains original encoded data or raw raster samples without
// invoking RenderImage. PDF objects, including decoded caches, remain unchanged.
func ExtractImageSource(opCtx context.Context, doc *model.Context, sd *types.StreamDict) (*ImageSource, error) {
	if source, handled, err := ExtractEncodedImageSource(opCtx, doc, sd); handled || err != nil {
		if err == nil {
			err = validateEncodedSource(source)
		}
		return source, err
	}
	source, err := imageRasterSourceFacts(doc, sd)
	if err != nil {
		return nil, err
	}
	w, h := source.Width, source.Height

	local := *sd
	local.Dict = sd.Dict.Clone().(types.Dict)
	local.Dict["Width"], local.Dict["Height"] = types.Integer(w), types.Integer(h)
	local.Content = nil
	source.Data, err = local.DecodeLengthWithLimit(-1, imageLimits(doc.XRefTable).MaxDecodeBytes)
	if err != nil {
		return nil, imageSourceDecodeError(err)
	}
	if len(source.Data) != source.RowStride*h {
		return nil, fmt.Errorf("%w: sample count %d, expected %d", ErrInvalidImageSource, len(source.Data), source.RowStride*h)
	}
	if err = contextutil.Check(opCtx); err != nil {
		return nil, err
	}
	return source, nil
}

func imageRasterSourceFacts(doc *model.Context, sd *types.StreamDict) (*ImageSource, error) {
	if sd.RawLength() > imageLimits(doc.XRefTable).MaxStreamBytes {
		return nil, ErrImageSourceLimit
	}
	if mask := sd.BooleanEntry("ImageMask"); mask != nil && *mask {
		return nil, fmt.Errorf("%w: image mask", ErrUnsupportedImageSource)
	}
	w, err := imageWidth(doc, sd, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidImageSource, err)
	}
	h, err := imageHeight(doc, sd, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidImageSource, err)
	}
	o, ok := sd.Find("BitsPerComponent")
	if !ok {
		return nil, fmt.Errorf("%w: missing BitsPerComponent", ErrInvalidImageSource)
	}
	bpc, err := doc.DereferenceInteger(o)
	if err != nil || bpc == nil {
		return nil, fmt.Errorf("%w: BitsPerComponent", ErrInvalidImageSource)
	}
	source := &ImageSource{Kind: ImageSourceRaster, Width: w, Height: h, BitsPerComponent: bpc.Value()}
	cs, err := doc.Dereference(sd.Dict["ColorSpace"])
	if err != nil {
		return nil, fmt.Errorf("%w: ColorSpace: %w", ErrInvalidImageSource, err)
	}
	if cs == nil && len(sd.FilterPipeline) > 0 && sd.FilterPipeline[len(sd.FilterPipeline)-1].Name == filter.CCITTFax {
		cs = types.Name(model.DeviceGrayCS)
	}
	if a, ok := cs.(types.Array); ok && len(a) > 0 && a[0] == types.Name(model.IndexedCS) {
		if len(a) != 4 || source.BitsPerComponent > 8 {
			return nil, fmt.Errorf("%w: Indexed layout", ErrUnsupportedImageSource)
		}
		source.ColorSpace, err = sourceComponentSpace(doc, a[1])
		if err != nil {
			return nil, err
		}
		count, e := doc.DereferenceInteger(a[2])
		if e != nil || count == nil || *count < 0 || *count > 255 {
			return nil, fmt.Errorf("%w: palette size", ErrInvalidImageSource)
		}
		lookup, e := sourceLookup(doc, a[3])
		if e != nil {
			return nil, e
		}
		expected := (count.Value() + 1) * sourceSpaceComponents(source.ColorSpace)
		if len(lookup) < expected {
			return nil, fmt.Errorf("%w: palette lookup length", ErrInvalidImageSource)
		}
		source.Palette, source.Components = lookup[:expected], 1
	} else {
		source.ColorSpace, err = sourceComponentSpace(doc, cs)
		if err != nil {
			return nil, err
		}
		source.Components = sourceSpaceComponents(source.ColorSpace)
	}
	if err = sourceRasterLimits(doc, w, h, source.Components, source.BitsPerComponent); err != nil {
		return nil, err
	}
	source.RowStride = int((int64(w)*int64(source.Components)*int64(source.BitsPerComponent) + 7) / 8)
	return source, nil
}

// WriteRasterImageSource emits original row-aligned samples to a caller sink.
// Facts/palette parsing and security policy are shared with ExtractImageSource;
// this entry never installs or returns a full decoded sample cache.
func WriteRasterImageSource(c context.Context, doc *model.Context, sd *types.StreamDict, writer io.Writer) (*ImageSource, error) {
	if err := contextutil.Check(c); err != nil {
		return nil, err
	}
	if err := requireContextWithXRefTable(doc); err != nil {
		return nil, err
	}
	if err := requireStreamDict(sd); err != nil {
		return nil, err
	}
	source, err := imageRasterSourceFacts(doc, sd)
	if err != nil {
		return nil, err
	}
	local := *sd
	local.Dict = sd.Dict.Clone().(types.Dict)
	local.Dict["Width"], local.Dict["Height"] = types.Integer(source.Width), types.Integer(source.Height)
	local.Content = nil
	expected := int64(source.RowStride) * int64(source.Height)
	// RenderImage consumes only the dictionary-sized plane for non-Indexed
	// images. Continue decoding trailing bytes to validate checksum/security,
	// while retaining that existing rendering contract. Indexed extraction
	// keeps its strict sample count.
	samples := &imageRasterSampleWriter{writer: writer, remaining: expected}
	n, err := local.WriteDecodedTo(c, samples, imageLimits(doc.XRefTable).MaxDecodeBytes)
	if err != nil {
		return nil, imageSourceDecodeError(err)
	}
	if n < expected || (len(source.Palette) > 0 && n != expected) {
		return nil, fmt.Errorf("%w: sample count %d, expected %d", ErrInvalidImageSource, n, expected)
	}
	return source, contextutil.Check(c)
}

func sourceSpaceComponents(cs string) int {
	switch cs {
	case model.DeviceGrayCS:
		return 1
	case model.DeviceRGBCS:
		return 3
	case model.DeviceCMYKCS:
		return 4
	}
	return 0
}

func sourceComponentSpace(doc *model.Context, o types.Object) (string, error) {
	o, err := doc.Dereference(o)
	if err != nil {
		return "", fmt.Errorf("%w: ColorSpace: %w", ErrInvalidImageSource, err)
	}
	if name, ok := o.(types.Name); ok && sourceSpaceComponents(string(name)) > 0 {
		return string(name), nil
	}
	if a, ok := o.(types.Array); ok && len(a) > 1 {
		switch a[0] {
		case types.Name(model.CalGrayCS):
			return model.DeviceGrayCS, nil
		case types.Name(model.CalRGBCS):
			return model.DeviceRGBCS, nil
		case types.Name(model.ICCBasedCS):
			profile, e := dereferenceRequiredStreamDict(doc.XRefTable, a[1], "ICCBased")
			if e != nil {
				return "", fmt.Errorf("%w: %w", ErrInvalidImageSource, e)
			}
			if n := profile.IntEntry("N"); n != nil {
				switch *n {
				case 1:
					return model.DeviceGrayCS, nil
				case 3:
					return model.DeviceRGBCS, nil
				case 4:
					return model.DeviceCMYKCS, nil
				}
			}
		}
	}
	return "", fmt.Errorf("%w: ColorSpace %T", ErrUnsupportedImageSource, o)
}

func sourceLookup(doc *model.Context, o types.Object) ([]byte, error) {
	o, err := doc.Dereference(o)
	if err != nil {
		return nil, fmt.Errorf("%w: palette: %w", ErrInvalidImageSource, err)
	}
	var data []byte
	switch v := o.(type) {
	case types.StringLiteral:
		data, err = types.Unescape(v.Value())
	case types.HexLiteral:
		data, err = v.Bytes()
	case types.StreamDict:
		local := v
		local.Dict = v.Dict.Clone().(types.Dict)
		local.Content = nil
		data, err = local.DecodeLengthWithLimit(-1, imageLimits(doc.XRefTable).MaxDecodeBytes)
		if err != nil {
			return nil, imageSourceDecodeError(err)
		}
	default:
		return nil, fmt.Errorf("%w: palette lookup %T", ErrUnsupportedImageSource, o)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: palette: %w", ErrInvalidImageSource, err)
	}
	return data, nil
}

// NewJBIG2ImageContext builds a private one-page PDF containing only this image
// and its globals. External decoders therefore never process unrelated images.
func NewJBIG2ImageContext(opCtx context.Context, source *ImageSource, conf *model.Configuration) (*model.Context, error) {
	if err := contextutil.Check(opCtx); err != nil {
		return nil, err
	}
	if source == nil || source.Kind != ImageSourceJBIG2 || len(source.Data) == 0 {
		return nil, ErrInvalidImageSource
	}
	if conf == nil {
		conf = model.NewDefaultConfiguration()
	}
	doc, err := CreateContextWithXRefTable(conf, &types.Dim{Width: float64(source.Width), Height: float64(source.Height)})
	if err != nil {
		return nil, err
	}
	if err = sourceRasterLimits(doc, source.Width, source.Height, 1, 1); err != nil {
		return nil, err
	}
	image := types.StreamDict{Dict: types.Dict{"Type": types.Name("XObject"), "Subtype": types.Name("Image"), "Width": types.Integer(source.Width), "Height": types.Integer(source.Height), "ColorSpace": types.Name(model.DeviceGrayCS), "BitsPerComponent": types.Integer(1), "Filter": types.Name(filter.JBIG2), "Length": types.Integer(len(source.Data))}, Raw: source.Data, FilterPipeline: []types.PDFFilter{{Name: filter.JBIG2}}}
	length := int64(len(source.Data))
	image.StreamLength = &length
	if len(source.Globals) > 0 {
		globals, e := doc.NewStreamDictForBuf(source.Globals)
		if e != nil {
			return nil, e
		}
		if e = globals.Encode(); e != nil {
			return nil, e
		}
		ref, e := doc.IndRefForNewObject(*globals)
		if e != nil {
			return nil, e
		}
		parms := types.Dict{"JBIG2Globals": *ref}
		image.Dict["DecodeParms"] = parms
		image.FilterPipeline[0].DecodeParms = parms
	}
	ref, err := doc.IndRefForNewObject(image)
	if err != nil {
		return nil, err
	}
	content, err := doc.NewStreamDictForBuf([]byte("q 1 0 0 1 0 0 cm /Im0 Do Q"))
	if err != nil {
		return nil, err
	}
	if err = content.Encode(); err != nil {
		return nil, err
	}
	contentRef, err := doc.IndRefForNewObject(*content)
	if err != nil {
		return nil, err
	}
	catalog, err := doc.Catalog()
	if err != nil {
		return nil, err
	}
	parent := catalog["Pages"]
	pages, err := doc.DereferenceDict(parent)
	if err != nil {
		return nil, err
	}
	page := types.Dict{"Type": types.Name("Page"), "Parent": parent, "Resources": types.Dict{"XObject": types.Dict{"Im0": *ref}}, "Contents": *contentRef}
	pageRef, err := doc.IndRefForNewObject(page)
	if err != nil {
		return nil, err
	}
	pages["Kids"], pages["Count"] = types.Array{*pageRef}, types.Integer(1)
	doc.PageCount = 1
	if err := contextutil.Check(opCtx); err != nil {
		return nil, err
	}
	return doc, nil
}

func validateEncodedSource(source *ImageSource) error {
	switch source.Kind {
	case ImageSourceJPEG:
		config, err := jpeg.DecodeConfig(bytes.NewReader(source.Data))
		if err != nil {
			return fmt.Errorf("%w: JPEG header: %w", ErrInvalidImageSource, err)
		}
		source.Width, source.Height, source.BitsPerComponent = config.Width, config.Height, 8
	case ImageSourceJP2, ImageSourceJ2K:
		data := source.Data
		if source.Kind == ImageSourceJP2 {
			found := false
			for offset := 0; offset < len(data); {
				remaining := len(data) - offset
				if remaining < 8 {
					return fmt.Errorf("%w: JP2 box header", ErrInvalidImageSource)
				}
				size := uint64(binary.BigEndian.Uint32(data[offset:]))
				header := 8
				if size == 1 {
					if remaining < 16 {
						return fmt.Errorf("%w: JP2 extended box header", ErrInvalidImageSource)
					}
					size = binary.BigEndian.Uint64(data[offset+8:])
					header = 16
				} else if size == 0 {
					size = uint64(remaining)
				}
				if size < uint64(header) || size > uint64(remaining) {
					return fmt.Errorf("%w: JP2 box length", ErrInvalidImageSource)
				}
				if string(data[offset+4:offset+8]) == "jp2c" {
					if found {
						return fmt.Errorf("%w: duplicate JP2 codestream", ErrInvalidImageSource)
					}
					found = true
					source.Width, source.Height, source.Components = 0, 0, 0
					if err := sourceJ2KHeader(source, data[offset+header:offset+int(size)]); err != nil {
						return err
					}
				}
				offset += int(size)
			}
			if !found {
				return fmt.Errorf("%w: missing JP2 codestream", ErrInvalidImageSource)
			}
		} else {
			return sourceJ2KHeader(source, data)
		}
	}
	return nil
}

func sourceJ2KHeader(source *ImageSource, data []byte) error {
	if len(data) < 42 || !bytes.Equal(data[:4], []byte{255, 79, 255, 81}) {
		return fmt.Errorf("%w: J2K SIZ header", ErrInvalidImageSource)
	}
	size := int(binary.BigEndian.Uint16(data[4:]))
	components := int(binary.BigEndian.Uint16(data[40:]))
	if components < 1 || size != 38+3*components || len(data) < size+4 {
		return fmt.Errorf("%w: J2K SIZ length", ErrInvalidImageSource)
	}
	x, y := binary.BigEndian.Uint32(data[8:]), binary.BigEndian.Uint32(data[12:])
	x0, y0 := binary.BigEndian.Uint32(data[16:]), binary.BigEndian.Uint32(data[20:])
	if x <= x0 || y <= y0 {
		return fmt.Errorf("%w: J2K dimensions", ErrInvalidImageSource)
	}
	source.Width, source.Height, source.Components = int(x-x0), int(y-y0), components
	source.BitsPerComponent = int(data[42]&127) + 1
	return nil
}

// IsStructuralReadError reports recognized damage to PDF object and stream
// boundaries. Image decode failures, cancellation and operational errors are
// deliberately excluded; callers must not send them to structural recovery.
func IsStructuralReadError(err error) bool {
	for _, candidate := range []error{ErrCorruptHeader, ErrMissingXRefSection, errCorruptDictObject, errCorruptObjectStream, errCorruptObjectStreamDict, errCorruptStreamDict, errCorruptStreamMarker, errCorruptTrailerDict, errCorruptXRefStream, errIncompleteXRefSubsection, errInvalidLastXRefSection, errMissingObjectStream, errMissingObjectStreamEntry, errMissingObjectStreamObjects, errMissingStreamLength, errMissingStreamOffset, errMissingTrailerDict, errMissingTrailerRoot, errMissingTrailerSize, errMissingXRefEOF, errMissingXRefStreamDict, errMissingXRefStreamLength, errTruncatedStreamMarker} {
		if errors.Is(err, candidate) {
			return true
		}
	}
	return false
}

// imageRasterSampleWriter forwards one sample plane and drains any decoded
// trailer without buffering it. Decode limits apply to the complete stream.
type imageRasterSampleWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *imageRasterSampleWriter) Write(data []byte) (int, error) {
	count := len(data)
	prefix := min(int64(count), w.remaining)
	if prefix > 0 {
		n, err := w.writer.Write(data[:prefix])
		w.remaining -= int64(n)
		if err != nil {
			return n, err
		}
		if int64(n) != prefix {
			return n, io.ErrShortWrite
		}
	}
	return count, nil
}
