package pdfcpu

import (
	"context"
	"io"
	"math"
	"strings"

	"github.com/pdfcpu/pdfcpu/internal/contextutil"
	"github.com/pdfcpu/pdfcpu/internal/filter/lzw"
	"github.com/pdfcpu/pdfcpu/pkg/filter"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

var inlineImageKeys = map[string]string{
	"BPC": "BitsPerComponent", "CS": "ColorSpace", "D": "Decode", "DP": "DecodeParms",
	"F": "Filter", "H": "Height", "IM": "ImageMask", "I": "Interpolate", "W": "Width",
}

func readInlineImage(ctx context.Context, doc *model.Context, input *string, resources types.Dict) (types.StreamDict, error) {
	dict := types.NewDict()
	for {
		*input = inlineSkipSpace(*input)
		if strings.HasPrefix(*input, "ID") && len(*input) > 2 && inlineWhitespace((*input)[2]) {
			crlf := strings.HasPrefix(*input, "ID\r\n")
			*input = (*input)[3:]
			if crlf {
				*input = (*input)[1:]
			}
			break
		}
		if len(*input) == 0 || (*input)[0] != '/' {
			return types.StreamDict{}, errInlineImagePreserved
		}
		key, err := model.ParseObject(ctx, input, 0, doc.MaxRecursionDepth())
		if err != nil {
			return types.StreamDict{}, errInlineImagePreserved
		}
		name, ok := key.(types.Name)
		if !ok {
			return types.StreamDict{}, errInlineImagePreserved
		}
		value, err := model.ParseObject(ctx, input, 0, doc.MaxRecursionDepth())
		if err != nil {
			return types.StreamDict{}, errInlineImagePreserved
		}
		if _, duplicate := dict[string(name)]; duplicate {
			return types.StreamDict{}, errInlineImagePreserved
		}
		dict[string(name)] = value
	}
	for short, full := range inlineImageKeys {
		if value, present := dict[short]; present {
			dict[full] = value
			delete(dict, short)
		}
	}
	width, height := dict.IntEntry("Width"), dict.IntEntry("Height")
	if width == nil || height == nil || *width <= 0 || *height <= 0 {
		return types.StreamDict{}, errInlineImagePreserved
	}
	if mask, present := dict["ImageMask"]; present {
		if _, ok := mask.(types.Boolean); !ok {
			return types.StreamDict{}, errInlineImagePreserved
		}
	}
	isMask := dict.BooleanEntry("ImageMask") != nil && *dict.BooleanEntry("ImageMask")
	bits := dict.IntEntry("BitsPerComponent")
	if (!isMask && (bits == nil || dict["ColorSpace"] == nil)) || (bits != nil && *bits != 1 && *bits != 2 && *bits != 4 && *bits != 8 && *bits != 16) || (isMask && bits != nil && *bits != 1) {
		return types.StreamDict{}, errInlineImagePreserved
	}
	if value, present := dict["Interpolate"]; present {
		if _, ok := value.(types.Boolean); !ok {
			return types.StreamDict{}, errInlineImagePreserved
		}
	}
	if value, present := dict["Intent"]; present {
		if _, ok := value.(types.Name); !ok {
			return types.StreamDict{}, errInlineImagePreserved
		}
	}
	if value, present := dict["Decode"]; present {
		array, ok := value.(types.Array)
		if !ok || len(array) == 0 || len(array)%2 != 0 {
			return types.StreamDict{}, errInlineImagePreserved
		}
		for _, number := range array {
			switch number.(type) {
			case types.Integer, types.Float:
			default:
				return types.StreamDict{}, errInlineImagePreserved
			}
		}
	}
	for key := range dict {
		switch key {
		case "Width", "Height", "BitsPerComponent", "ColorSpace", "Decode", "DecodeParms", "Filter", "ImageMask", "Interpolate", "Intent":
		default:
			return types.StreamDict{}, errInlineImagePreserved
		}
	}
	if color, present := dict["ColorSpace"]; present {
		resolved, err := inlineColorSpace(doc, color, resources)
		if err != nil {
			return types.StreamDict{}, err
		}
		dict["ColorSpace"] = resolved
	}
	switch value := dict["Filter"].(type) {
	case nil:
	case types.Name:
		dict["Filter"] = types.Name(inlineFilterName(string(value)))
	case types.Array:
		canonical := make(types.Array, len(value))
		for index, item := range value {
			name, ok := item.(types.Name)
			if !ok {
				return types.StreamDict{}, errInlineImagePreserved
			}
			canonical[index] = types.Name(inlineFilterName(string(name)))
		}
		dict["Filter"] = canonical
	default:
		return types.StreamDict{}, errInlineImagePreserved
	}
	pipeline, err := pdfFilterPipeline(ctx, doc, dict)
	if err != nil {
		return types.StreamDict{}, errInlineImagePreserved
	}
	length, err := inlineDataLength(ctx, doc, dict, pipeline, *input)
	if err != nil {
		return types.StreamDict{}, err
	}
	if length < 0 || length >= len(*input) || !inlineWhitespace((*input)[length]) {
		return types.StreamDict{}, errInlineImagePreserved
	}
	rest := strings.TrimLeftFunc((*input)[length:], func(value rune) bool { return value <= 255 && inlineWhitespace(byte(value)) })
	if !strings.HasPrefix(rest, "EI") || (len(rest) > 2 && !inlineDelimiter(rest[2])) {
		return types.StreamDict{}, errInlineImagePreserved
	}
	dict["Type"], dict["Subtype"] = types.Name("XObject"), types.Name("Image")
	dict["Length"] = types.Integer(length)
	streamLength := int64(length)
	image := types.StreamDict{Dict: dict, Raw: []byte((*input)[:length]), StreamLength: &streamLength, FilterPipeline: pipeline}
	*input = rest[2:]
	return image, nil
}

func inlineFilterName(name string) string {
	if canonical, ok := inlineImageFilterAliases[name]; ok {
		return canonical
	}
	return name
}

func inlineColorSpace(doc *model.Context, color types.Object, resources types.Dict) (types.Object, error) {
	return inlineScopedColorSpace(doc, color, resources, 0)
}

func inlineScopedColorSpace(doc *model.Context, color types.Object, resources types.Dict, depth int) (types.Object, error) {
	if err := doc.CheckRecursionDepth("inline image color space", depth); err != nil {
		return nil, errInlineImagePreserved
	}
	resolved, err := doc.Dereference(color)
	if err != nil {
		return nil, errInlineImagePreserved
	}
	if name, ok := resolved.(types.Name); ok {
		switch name {
		case "G":
			name = model.DeviceGrayCS
		case "RGB":
			name = model.DeviceRGBCS
		case "CMYK":
			name = model.DeviceCMYKCS
		}
		if colorSpaceNameComponents(name) != 0 {
			return inlineDeviceColorSpace(doc, name, resources)
		}
		spaces, err := doc.DereferenceDict(resources["ColorSpace"])
		if err != nil || spaces[string(name)] == nil {
			return nil, errInlineImagePreserved
		}
		return inlineScopedColorSpace(doc, spaces[string(name)], resources, depth+1)
	}
	if array, ok := resolved.(types.Array); ok && len(array) > 0 {
		if array[0] == types.Name("I") || array[0] == types.Name(model.IndexedCS) {
			if len(array) != 4 {
				return nil, errInlineImagePreserved
			}
			base, err := inlineScopedColorSpace(doc, array[1], resources, depth+1)
			if err != nil {
				return nil, err
			}
			return types.Array{types.Name(model.IndexedCS), base, array[2], array[3]}, nil
		}
		return color, nil
	}
	return nil, errInlineImagePreserved
}

func inlineDeviceColorSpace(doc *model.Context, name types.Name, resources types.Dict) (types.Object, error) {
	spaces, err := doc.DereferenceDict(resources["ColorSpace"])
	if err != nil {
		return nil, errInlineImagePreserved
	}
	color := spaces["Default"+strings.TrimPrefix(string(name), "Device")]
	if color == nil {
		return name, nil
	}
	resolved, err := doc.Dereference(color)
	if err != nil {
		return nil, errInlineImagePreserved
	}
	switch value := resolved.(type) {
	case types.Name:
		if colorSpaceNameComponents(value) != colorSpaceNameComponents(name) {
			return nil, errInlineImagePreserved
		}
	case types.Array:
		family, err := colorSpaceArrayName(doc.XRefTable, value)
		if err != nil || !types.MemberOf(family.Value(), []string{model.CalGrayCS, model.CalRGBCS, model.ICCBasedCS, model.SeparationCS, model.DeviceNCS}) {
			return nil, errInlineImagePreserved
		}
	default:
		return nil, errInlineImagePreserved
	}
	components, err := ColorSpaceComponents(doc.XRefTable, &types.StreamDict{Dict: types.Dict{"ColorSpace": color}})
	if err != nil || components != colorSpaceNameComponents(name) {
		return nil, errInlineImagePreserved
	}
	return color, nil
}

func inlineRawLength(doc *model.Context, dict types.Dict) (int, error) {
	width, height, bits := dict.IntEntry("Width"), dict.IntEntry("Height"), dict.IntEntry("BitsPerComponent")
	channels := 0
	if mask := dict.BooleanEntry("ImageMask"); mask != nil && *mask {
		channels = 1
		value := 1
		bits = &value
	} else {
		color, err := doc.Dereference(dict["ColorSpace"])
		if err != nil {
			return 0, errInlineImagePreserved
		}
		if array, ok := color.(types.Array); ok && len(array) > 0 && array[0] == types.Name(model.IndexedCS) {
			channels = 1
		} else {
			channels, err = ColorSpaceComponents(doc.XRefTable, &types.StreamDict{Dict: dict})
			if err != nil {
				return 0, errInlineImagePreserved
			}
		}
	}
	if width == nil || height == nil || bits == nil || *width <= 0 || *height <= 0 || channels == 0 || (*bits != 1 && *bits != 2 && *bits != 4 && *bits != 8 && *bits != 16) {
		return 0, errInlineImagePreserved
	}
	maxBytes := int64(^uint(0) >> 1)
	if int64(*width) > (maxBytes-7)/int64(channels)/int64(*bits) || (doc.Limits.MaxImagePixels > 0 && int64(*width) > doc.Limits.MaxImagePixels/int64(*height)) {
		return 0, errInlineImagePreserved
	}
	rowBytes := (int64(*width)*int64(channels)*int64(*bits) + 7) / 8
	if rowBytes > maxBytes/int64(*height) {
		return 0, errInlineImagePreserved
	}
	length := rowBytes * int64(*height)
	if doc.Limits.MaxImageBytes > 0 && length > doc.Limits.MaxImageBytes {
		return 0, errInlineImagePreserved
	}
	return int(length), nil
}

func inlineDataLength(ctx context.Context, doc *model.Context, dict types.Dict, pipeline []types.PDFFilter, data string) (int, error) {
	if len(pipeline) == 0 {
		return inlineRawLength(doc, dict)
	}
	limit := doc.Limits.MaxDecodeBytes
	if limit <= 0 {
		limit = filter.DefaultMaxDecodeBytes
	}
	switch pipeline[0].Name {
	case filter.Flate, filter.LZW:
		source := strings.NewReader(data)
		defer source.Reset("")
		var reader io.Reader
		var release func(bool) error
		if pipeline[0].Name == filter.Flate {
			decoder, err := filter.AcquireZlibReader(source)
			if err != nil {
				return 0, errInlineImagePreserved
			}
			reader = decoder
			release = func(reusable bool) error { return filter.ReleaseZlibReader(decoder, reusable) }
		} else {
			early := pipeline[0].DecodeParms.IntEntry("EarlyChange")
			decoder := lzw.NewReader(source, early == nil || *early == 1)
			reader = decoder
			release = func(bool) error { return decoder.Close() }
		}
		maxRead := limit
		if maxRead < math.MaxInt64 {
			maxRead++
		}
		decoded, err := io.Copy(io.Discard, io.LimitReader(contextutil.Reader(ctx, reader), maxRead))
		closeErr := release(err == nil && decoded <= limit)
		if err != nil || closeErr != nil || decoded > limit {
			return 0, errInlineImagePreserved
		}
		return len(data) - source.Len(), nil
	case filter.ASCIIHex:
		for index := 0; index < len(data); index++ {
			if index%4096 == 0 && ctx.Err() != nil {
				return 0, ctx.Err()
			}
			if data[index] == '>' {
				return index + 1, nil
			}
			if !inlineWhitespace(data[index]) && !strings.ContainsRune("0123456789abcdefABCDEF", rune(data[index])) {
				return 0, errInlineImagePreserved
			}
		}
	case filter.ASCII85:
		if end := strings.Index(data, "~>"); end >= 0 {
			return end + 2, nil
		}
	case filter.RunLength:
		var decoded int64
		for offset := 0; offset < len(data); {
			control := int(data[offset])
			offset++
			if control == 128 {
				return offset, nil
			}
			if control < 128 {
				offset += control + 1
				decoded += int64(control + 1)
			} else {
				offset++
				decoded += int64(257 - control)
			}
			if decoded > limit || ctx.Err() != nil {
				return 0, errInlineImagePreserved
			}
		}
	case filter.DCT:
		return inlineJPEGLength(ctx, data)
	}
	return 0, errInlineImagePreserved
}

func inlineJPEGLength(ctx context.Context, data string) (int, error) {
	if !strings.HasPrefix(data, "\xff\xd8") {
		return 0, errInlineImagePreserved
	}
	entropy := false
	for offset := 2; offset < len(data); {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if entropy {
			next := strings.IndexByte(data[offset:], 0xff)
			if next < 0 {
				break
			}
			offset += next
		}
		if data[offset] != 0xff {
			break
		}
		for offset < len(data) && data[offset] == 0xff {
			offset++
		}
		if offset == len(data) {
			break
		}
		marker := data[offset]
		offset++
		if marker == 0xd9 {
			return offset, nil
		}
		if entropy && (marker == 0 || marker >= 0xd0 && marker <= 0xd7) {
			continue
		}
		if offset+2 > len(data) {
			break
		}
		length := int(data[offset])<<8 | int(data[offset+1])
		if length < 2 || length > len(data)-offset {
			break
		}
		offset += length
		entropy = marker == 0xda
	}
	return 0, errInlineImagePreserved
}
