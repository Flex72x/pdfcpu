package pdfcpu

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"reflect"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/filter"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func sourceTestContext(t *testing.T) *model.Context {
	t.Helper()
	doc, err := CreateContextWithXRefTable(model.NewDefaultConfiguration(), &types.Dim{Width: 100, Height: 100})
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func sourceTestStream(data []byte, cs types.Object, bpc, w, h int) *types.StreamDict {
	return &types.StreamDict{Dict: types.Dict{"Width": types.Integer(w), "Height": types.Integer(h), "BitsPerComponent": types.Integer(bpc), "ColorSpace": cs}, Raw: data, Content: []byte("stale decoded cache")}
}

func sourceTestFlate(t *testing.T, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w := zlib.NewWriter(&out)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestImageSourceJPEGPreservesEncodedBytes(t *testing.T) {
	doc := sourceTestContext(t)
	for _, im := range []image.Image{image.NewGray(image.Rect(0, 0, 1, 1)), image.NewRGBA(image.Rect(0, 0, 13, 11))} {
		var encoded bytes.Buffer
		if err := jpeg.Encode(&encoded, im, nil); err != nil {
			t.Fatal(err)
		}
		for _, chain := range []string{"direct", "flate", "hex-flate"} {
			data := encoded.Bytes()
			pipeline := []types.PDFFilter{{Name: filter.DCT}}
			if chain != "direct" {
				data = sourceTestFlate(t, data)
				pipeline = append([]types.PDFFilter{{Name: filter.Flate}}, pipeline...)
			}
			if chain == "hex-flate" {
				data = append([]byte(hex.EncodeToString(data)), '>')
				pipeline = append([]types.PDFFilter{{Name: filter.ASCIIHex}}, pipeline...)
			}
			sd := sourceTestStream(data, types.Name(model.DeviceCMYKCS), 8, 1, 1)
			sd.CSComponents = 4
			sd.FilterPipeline = pipeline
			sd.Dict["SMask"] = types.IndirectRef{ObjectNumber: 999}
			sd.Dict["Decode"] = types.NewIntegerArray(1, 0)
			sd.Dict["Matte"] = types.NewIntegerArray(1)
			before := *sd
			before.Dict = sd.Dict.Clone().(types.Dict)
			for range 2 {
				got, err := ExtractImageSource(t.Context(), doc, sd)
				if err != nil || got.Kind != ImageSourceJPEG || !bytes.Equal(got.Data, encoded.Bytes()) {
					t.Fatalf("%s: %+v %v", chain, got, err)
				}
			}
			if !reflect.DeepEqual(*sd, before) {
				t.Fatal("source mutated")
			}
		}
	}
	// The opaque API must leave compressor recovery possible for damaged headers.
	sd := sourceTestStream([]byte("invalid jpeg"), types.Name(model.DeviceRGBCS), 8, 1, 1)
	sd.FilterPipeline = []types.PDFFilter{{Name: filter.DCT}}
	if _, handled, err := ExtractEncodedImageSource(t.Context(), doc, sd); !handled || err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractImageSource(t.Context(), doc, sd); !errors.Is(err, ErrInvalidImageSource) {
		t.Fatal(err)
	}
}

func TestImageSourceRasterLayoutsAndNoRendering(t *testing.T) {
	doc := sourceTestContext(t)
	for _, tc := range []struct {
		cs         string
		components int
	}{{model.DeviceGrayCS, 1}, {model.DeviceRGBCS, 3}, {model.DeviceCMYKCS, 4}} {
		for _, bpc := range []int{1, 2, 4, 8, 16} {
			stride := (3*tc.components*bpc + 7) / 8
			samples := bytes.Repeat([]byte{0x5a}, stride*2)
			for _, flate := range []bool{false, true} {
				sd := sourceTestStream(samples, types.Name(tc.cs), bpc, 3, 2)
				if flate {
					sd.Raw = sourceTestFlate(t, samples)
					sd.FilterPipeline = []types.PDFFilter{{Name: filter.Flate}}
				}
				sd.Dict["Decode"] = types.NewIntegerArray(1, 0)
				sd.Dict["SMask"] = types.IndirectRef{ObjectNumber: 999}
				sd.Dict["Mask"] = types.IndirectRef{ObjectNumber: 998}
				sd.Dict["Matte"] = types.NewIntegerArray(1)
				before := *sd
				before.Dict = sd.Dict.Clone().(types.Dict)
				source, err := ExtractImageSource(t.Context(), doc, sd)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(source.Data, samples) || source.Components != tc.components || source.RowStride != stride || source.BitsPerComponent != bpc {
					t.Fatalf("wrong samples: %+v", source)
				}
				if !reflect.DeepEqual(*sd, before) {
					t.Fatal("source mutated")
				}
			}
		}
	}
}

func TestImageSourcePaletteAndICCComponents(t *testing.T) {
	doc := sourceTestContext(t)
	lookup := []byte{0, 0, 0, 255, 127, 63}
	for _, o := range []types.Object{types.StringLiteral(string(lookup)), types.HexLiteral(hex.EncodeToString(lookup))} {
		sd := sourceTestStream([]byte{0x40}, types.Array{types.Name(model.IndexedCS), types.Name(model.DeviceRGBCS), types.Integer(1), o}, 1, 2, 1)
		source, err := ExtractImageSource(t.Context(), doc, sd)
		if err != nil || !bytes.Equal(source.Palette, lookup) || source.Components != 1 {
			t.Fatalf("%+v %v", source, err)
		}
	}
	profile := types.StreamDict{Dict: types.Dict{"N": types.Integer(4)}, Raw: []byte("not an ICC profile")}
	ref, err := doc.IndRefForNewObject(profile)
	if err != nil {
		t.Fatal(err)
	}
	sd := sourceTestStream([]byte{1, 2, 3, 4}, types.Array{types.Name(model.ICCBasedCS), *ref}, 8, 1, 1)
	source, err := ExtractImageSource(t.Context(), doc, sd)
	if err != nil || source.ColorSpace != model.DeviceCMYKCS || source.Components != 4 {
		t.Fatalf("%+v %v", source, err)
	}
}

func TestImageSourceErrorsLimitsAndCancellation(t *testing.T) {
	doc := sourceTestContext(t)
	sd := sourceTestStream([]byte{0}, types.Name(model.DeviceGrayCS), 8, 2, 1)
	if _, err := ExtractImageSource(t.Context(), doc, sd); !errors.Is(err, ErrInvalidImageSource) {
		t.Fatal(err)
	}
	sd.Dict["ColorSpace"] = types.Name("DeviceN")
	if _, err := ExtractImageSource(t.Context(), doc, sd); !errors.Is(err, ErrUnsupportedImageSource) {
		t.Fatal(err)
	}
	sd = sourceTestStream(sourceTestFlate(t, bytes.Repeat([]byte{0}, 4096)), types.Name(model.DeviceGrayCS), 8, 64, 64)
	sd.FilterPipeline = []types.PDFFilter{{Name: filter.Flate}}
	doc.Conf.Limits.MaxDecodeBytes = 32
	if _, err := ExtractImageSource(t.Context(), doc, sd); !errors.Is(err, ErrImageSourceLimit) || !errors.Is(err, filter.ErrDecodeLimitExceeded) {
		t.Fatal(err)
	}
	sd = sourceTestStream([]byte{0}, types.Name(model.DeviceGrayCS), 8, 1<<30, 1<<30)
	if _, err := ExtractImageSource(t.Context(), doc, sd); !errors.Is(err, ErrImageSourceLimit) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ExtractImageSource(ctx, doc, sd); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := ExtractImageSource(t.Context(), nil, sd); !errors.Is(err, ErrMissingPDFContext) {
		t.Fatal(err)
	}
	if _, err := ExtractImageSource(nil, doc, sd); !errors.Is(err, model.ErrMissingContext) {
		t.Fatal(err)
	}
	if _, err := ExtractImageSource(t.Context(), doc, nil); !errors.Is(err, ErrMissingStreamDict) {
		t.Fatal(err)
	}
}

func TestImageSourceJBIG2GlobalsAndPrivateContext(t *testing.T) {
	doc := sourceTestContext(t)
	globals := sourceTestStream(sourceTestFlate(t, []byte("shared globals")), nil, 0, 0, 0)
	globals.FilterPipeline = []types.PDFFilter{{Name: filter.Flate}}
	ref, err := doc.IndRefForNewObject(*globals)
	if err != nil {
		t.Fatal(err)
	}
	sd := sourceTestStream([]byte("image segments"), types.Name(model.DeviceGrayCS), 1, 3, 2)
	sd.FilterPipeline = []types.PDFFilter{{Name: filter.JBIG2, DecodeParms: types.Dict{"JBIG2Globals": *ref}}}
	source, err := ExtractImageSource(t.Context(), doc, sd)
	if err != nil || !bytes.Equal(source.Globals, []byte("shared globals")) {
		t.Fatalf("%+v %v", source, err)
	}
	private, err := NewJBIG2ImageContext(t.Context(), source, nil)
	if err != nil || private.PageCount != 1 || private == doc {
		t.Fatalf("%+v %v", private, err)
	}
	if string(globals.Content) != "stale decoded cache" {
		t.Fatal("globals mutated")
	}
}

func TestImageSourceJPXHeadersAndOuterFilters(t *testing.T) {
	doc := sourceTestContext(t)
	// SOC/SIZ header for a one-component 3x2 codestream. Sample decode is a
	// consumer responsibility; header validation must not decode the stream.
	j2k := make([]byte, 45)
	copy(j2k, []byte{255, 79, 255, 81})
	j2k[5] = 41
	j2k[11] = 3
	j2k[15] = 2
	j2k[27] = 3
	j2k[31] = 2
	j2k[41] = 1
	j2k[42] = 7
	j2k[43] = 1
	j2k[44] = 1
	jp2 := append([]byte{0, 0, 0, 12, 'j', 'P', ' ', ' ', 13, 10, 135, 10}, []byte{0, 0, 0, 53, 'j', 'p', '2', 'c'}...)
	jp2 = append(jp2, j2k...)
	for _, data := range [][]byte{j2k, jp2} {
		for _, flate := range []bool{false, true} {
			sd := sourceTestStream(data, nil, 0, 0, 0)
			sd.FilterPipeline = []types.PDFFilter{{Name: filter.JPX}}
			if flate {
				sd.Raw = sourceTestFlate(t, data)
				sd.FilterPipeline = append([]types.PDFFilter{{Name: filter.Flate}}, sd.FilterPipeline...)
			}
			source, err := ExtractImageSource(t.Context(), doc, sd)
			if err != nil || !bytes.Equal(source.Data, data) || source.Width != 3 || source.Height != 2 || source.Components != 1 {
				t.Fatalf("%+v %v", source, err)
			}
		}
	}
	for _, data := range [][]byte{j2k[:8], jp2[:20], append(bytes.Clone(jp2), 0)} {
		sd := sourceTestStream(data, nil, 0, 0, 0)
		sd.FilterPipeline = []types.PDFFilter{{Name: filter.JPX}}
		if _, err := ExtractImageSource(t.Context(), doc, sd); !errors.Is(err, ErrInvalidImageSource) {
			t.Fatal(err)
		}
	}
}

func TestImageSourceRasterLZWRunLengthAndIndirectDimensions(t *testing.T) {
	doc := sourceTestContext(t)
	for _, name := range []string{filter.LZW, filter.RunLength} {
		samples := []byte{0, 0, 0, 255, 128, 64}
		sd := sourceTestStream(nil, types.Name(model.DeviceRGBCS), 8, 2, 1)
		sd.Content = samples
		sd.FilterPipeline = []types.PDFFilter{{Name: name}}
		if err := sd.Encode(); err != nil {
			t.Fatal(err)
		}
		ref, err := doc.IndRefForNewObject(types.Integer(1))
		if err != nil {
			t.Fatal(err)
		}
		sd.Dict["Height"] = *ref
		source, err := ExtractImageSource(t.Context(), doc, sd)
		if err != nil || !bytes.Equal(source.Data, samples) {
			t.Fatalf("%s: %+v %v", name, source, err)
		}
	}
}

func TestImageSourceRecoveryClassification(t *testing.T) {
	if !IsStructuralReadError(fmt.Errorf("wrapped: %w", ErrMissingXRefSection)) {
		t.Fatal("structural damage lost")
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, filter.ErrDecodeLimitExceeded, ErrImageSourceLimit, ErrInvalidImageSource, ErrUnsupportedImageSource, errObjectBufferLimit, errMissingReadSeeker} {
		if IsStructuralReadError(err) {
			t.Fatalf("nonstructural error eligible for recovery: %v", err)
		}
	}
}
