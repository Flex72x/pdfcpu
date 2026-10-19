package pdfcpu

import (
	"bytes"
	"context"
	"encoding/ascii85"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/filter"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func inlineTestDocument(t *testing.T, contents ...string) (*model.Context, types.Dict) {
	t.Helper()
	doc := sourceTestContext(t)
	root, err := doc.Pages()
	if err != nil {
		t.Fatal(err)
	}
	pages, err := doc.DereferenceDict(*root)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := doc.EmptyPage(root, types.RectForFormat("A4"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := model.AppendPageTree(reference, 1, pages); err != nil {
		t.Fatal(err)
	}
	page, err := doc.DereferenceDict(*reference)
	if err != nil {
		t.Fatal(err)
	}
	var streams types.Array
	for _, content := range contents {
		reference, err := doc.StreamDictIndRef([]byte(content))
		if err != nil {
			t.Fatal(err)
		}
		streams = append(streams, *reference)
	}
	page["Contents"] = streams
	doc.PageCount = 1
	return doc, page
}

func TestExternalizeInlineImagesPreservesSamplesAndContent(t *testing.T) {
	samples := []byte(" EI Q \x00\xff BI ID ")
	flate := sourceTestFlate(t, samples)
	lzwFilter, err := filter.NewFilter(filter.LZW, nil)
	if err != nil {
		t.Fatal(err)
	}
	lzwReader, err := lzwFilter.Encode(bytes.NewReader(samples))
	if err != nil {
		t.Fatal(err)
	}
	lzwData, err := io.ReadAll(lzwReader)
	if err != nil {
		t.Fatal(err)
	}
	var ascii bytes.Buffer
	encoder := ascii85.NewEncoder(&ascii)
	if _, err := encoder.Write(flate); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	ascii.WriteString("~>")
	for _, test := range []struct {
		name, dictionary string
		data             []byte
	}{
		{"raw", "", samples},
		{"flate", "/F /Fl", flate},
		{"lzw", "/F /LZW", lzwData},
		{"hex-flate", "/F [/AHx /Fl]", []byte(hex.EncodeToString(flate) + ">")},
		{"ascii85-flate", "/F [/A85 /Fl]", ascii.Bytes()},
		{"runlength", "/F /RL", append(append([]byte{byte(len(samples) - 1)}, samples...), 128)},
	} {
		t.Run(test.name, func(t *testing.T) {
			header := fmt.Sprintf("q BI /W %d /H 1 /CS /G /BPC 8 %s ID ", len(samples), test.dictionary)
			doc, page := inlineTestDocument(t, header+string(test.data)+"\r\nEI\r ", "\nQ\n/Existing Do")
			resources := types.Dict{"XObject": types.Dict{"Existing": types.Name("keep")}}
			page["Resources"] = resources
			before := resources.Clone()
			count, err := ExternalizeInlineImages(t.Context(), doc)
			if err != nil || count != 1 {
				t.Fatalf("count = %d, err = %v", count, err)
			}
			if !reflect.DeepEqual(resources, before) {
				t.Fatal("shared resources were mutated")
			}
			content, err := doc.PageContent(page, 1)
			if err != nil || !bytes.Contains(content, []byte("Q\n/Existing Do")) || bytes.Contains(content, []byte("BI")) {
				t.Fatalf("content = %q, err = %v", content, err)
			}
			objects := page.DictEntry("Resources").DictEntry("XObject")
			for name, reference := range objects {
				if name == "Existing" {
					continue
				}
				stream, _, err := doc.DereferenceStreamDict(reference)
				if err != nil || !bytes.Equal(stream.Raw, test.data) {
					t.Fatalf("encoded samples changed: %v", err)
				}
				if err := stream.DecodeWithLimit(doc.Limits.MaxDecodeBytes); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(stream.Content, samples) {
					t.Fatalf("samples = %q, want %q", stream.Content, samples)
				}
			}
			if count, err := ExternalizeInlineImages(t.Context(), doc); count != 0 || err != nil {
				t.Fatalf("not idempotent: %d, %v", count, err)
			}
		})
	}
}

func TestExternalizeInlineImagesPreservesUncertainStreams(t *testing.T) {
	valid := "BI /W 1 /H 1 /BPC 8 /CS /G ID x EI Q "
	for _, content := range []string{
		valid + "BI /W 1 /H 1 /BPC 8 /CS /G /F /Unknown ID abc EI Q",
		valid + "BI /W 1 /H 1 /BPC 8 /CS /G /F /CCF ID abc EI Q",
		valid + "BI /W 1 /H 1 /BPC 8 /CS /G /F /Fl ID broken EI Q",
		valid + "BI /W 1 /H 1 /BPC 8 /CS /G ID x EIx Q",
		valid + "BI /W 1 /H 1 /BPC 8 /CS /G ID x",
		valid + "BI /W 1 /W 2 /H 1 /BPC 8 /CS /G ID x EI Q",
		valid + "BI /W 1 /H 1 /BPC 3 /CS /G ID x EI Q",
		valid + "BI /W 1 /H 1 /BPC 8 /CS /G /D [0 (1)] ID x EI Q",
		valid + "BI /W 1 /H 1 /BPC 8 /CS /G /I 1 ID x EI Q",
		valid + "BI /W 1 /H 1 /IM true /BPC 8 ID x EI Q",
		valid + "BI /W 9223372036854775807 /H 2 /BPC 16 /CS /CMYK ID x EI Q",
		"( BI /W 1 ID fake EI ) Tj /Span <</ActualText (BI ID EI)>> BDC EMC",
		"% BI ID fake EI\n/BI Do",
	} {
		t.Run(fmt.Sprintf("%x", content[len(content)/2:]), func(t *testing.T) {
			doc, page := inlineTestDocument(t, content)
			before := len(doc.Table)
			count, err := ExternalizeInlineImages(t.Context(), doc)
			if err != nil || count != 0 || len(doc.Table) != before {
				t.Fatalf("uncertain stream changed: count=%d, err=%v", count, err)
			}
			actual, err := doc.PageContent(page, 1)
			if err != nil || string(actual) != content {
				t.Fatalf("content changed: %v", err)
			}
		})
	}
}

func TestExternalizeInlineImageColorSpaceAndAbbreviationPrecedence(t *testing.T) {
	doc, page := inlineTestDocument(t, "BI /Width 99 /W 1 /H 1 /CS [/I /RGB 1 <000000ffffff>] /BPC 1 /D [1 0] /I true ID \x80 EI Q")
	count, err := ExternalizeInlineImages(t.Context(), doc)
	if err != nil || count != 1 {
		t.Fatalf("count=%d, err=%v", count, err)
	}
	for _, reference := range page.DictEntry("Resources").DictEntry("XObject") {
		stream, _, err := doc.DereferenceStreamDict(reference)
		if err != nil {
			t.Fatal(err)
		}
		if *stream.IntEntry("Width") != 1 || !*stream.BooleanEntry("Interpolate") || !reflect.DeepEqual(stream.ArrayEntry("Decode"), types.NewIntegerArray(1, 0)) {
			t.Fatal("inline dictionary semantics changed")
		}
		if space := stream.ArrayEntry("ColorSpace"); space[0] != types.Name(model.IndexedCS) || space[1] != types.Name(model.DeviceRGBCS) {
			t.Fatalf("space = %v", space)
		}
	}
}

func TestExternalizeInlineImageJPEGBoundaries(t *testing.T) {
	var jpegData bytes.Buffer
	if err := jpeg.Encode(&jpegData, image.NewGray(image.Rect(0, 0, 8, 8)), nil); err != nil {
		t.Fatal(err)
	}
	data := append([]byte{0xff, 0xd8, 0xff, 0xfe, 0, 10}, []byte(" EI Q BI")...)
	data = append(data, jpegData.Bytes()[2:]...)
	doc, page := inlineTestDocument(t, "BI /W 8 /H 8 /CS /G /BPC 8 /F /DCT ID "+string(data)+" EI /After Do")
	count, err := ExternalizeInlineImages(t.Context(), doc)
	if err != nil || count != 1 {
		t.Fatalf("count=%d, err=%v", count, err)
	}
	for _, reference := range page.DictEntry("Resources").DictEntry("XObject") {
		stream, _, err := doc.DereferenceStreamDict(reference)
		if err != nil || !bytes.Equal(stream.Raw, data) || stream.FilterPipeline[0].Name != filter.DCT {
			t.Fatalf("JPEG changed: %v", err)
		}
	}
}

func TestExternalizeInlineImagesLimitsAndCancellation(t *testing.T) {
	for _, limit := range []string{"decode", "objects"} {
		doc, page := inlineTestDocument(t, "BI /W 1000 /H 1 /CS /G /BPC 8 /F /Fl ID "+string(sourceTestFlate(t, []byte(strings.Repeat("x", 1000))))+" EI Q")
		if limit == "decode" {
			doc.Limits.MaxDecodeBytes = 500
		} else {
			doc.Limits.MaxObjectCount = *doc.Size
		}
		before := len(doc.Table)
		if count, err := ExternalizeInlineImages(t.Context(), doc); count != 0 || err != nil || len(doc.Table) != before {
			t.Fatalf("%s: count=%d, err=%v", limit, count, err)
		}
		if _, ok := page["Contents"].(types.Array); !ok {
			t.Fatal("limited stream changed")
		}
	}
	doc, _ := inlineTestDocument(t, "BI /W 1 /H 1 /CS /G /BPC 8 ID x EI Q")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ExternalizeInlineImages(ctx, doc); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := ExternalizeInlineImages(nil, doc); !errors.Is(err, model.ErrMissingContext) {
		t.Fatal(err)
	}
}

func TestExternalizeInlineImagesScopedResourcesAndSharedForms(t *testing.T) {
	doc, page := inlineTestDocument(t, "q /Form Do Q /Form Do")
	doc.Limits.MaxRecursionDepth = 1
	root, err := doc.Pages()
	if err != nil {
		t.Fatal(err)
	}
	pages, err := doc.DereferenceDict(*root)
	if err != nil {
		t.Fatal(err)
	}
	pageType, err := doc.IndRefForNewObject(types.Name("Pages"))
	if err != nil {
		t.Fatal(err)
	}
	pages["Type"] = *pageType
	formContent := "BI /W 1 /H 1 /BPC 8 /CS /Local ID x EI Q"
	form, err := doc.NewStreamDictForBuf([]byte(formContent))
	if err != nil {
		t.Fatal(err)
	}
	local := types.Dict{"ColorSpace": types.Dict{"Local": types.Name(model.DeviceGrayCS)}}
	formType, err := doc.IndRefForNewObject(types.Name("Form"))
	if err != nil {
		t.Fatal(err)
	}
	form.Dict["Type"], form.Dict["Subtype"] = types.Name("XObject"), *formType
	form.Dict["BBox"] = types.NewIntegerArray(0, 0, 100, 100)
	form.Dict["Resources"] = local
	if err := form.Encode(); err != nil {
		t.Fatal(err)
	}
	reference, err := doc.IndRefForNewObject(*form)
	if err != nil {
		t.Fatal(err)
	}
	page["Resources"] = types.Dict{"XObject": types.Dict{"Form": *reference}, "ColorSpace": types.Dict{"Local": types.Name(model.DeviceRGBCS)}}
	count, err := ExternalizeInlineImages(t.Context(), doc)
	if err != nil || count != 1 {
		t.Fatalf("shared form count=%d, err=%v", count, err)
	}
	updated, _, err := doc.DereferenceStreamDict(*reference)
	if err != nil {
		t.Fatal(err)
	}
	for _, imageReference := range updated.Dict.DictEntry("Resources").DictEntry("XObject") {
		imageStream, _, err := doc.DereferenceStreamDict(imageReference)
		if err != nil || imageStream.Dict["ColorSpace"] != types.Name(model.DeviceGrayCS) {
			t.Fatalf("wrong resource scope: %v", err)
		}
	}
	if local["XObject"] != nil || page.DictEntry("Resources").DictEntry("XObject")["Form"] != *reference {
		t.Fatal("shared resource dictionary changed")
	}
	outer := form.Clone().(types.StreamDict)
	outer.Content = []byte("q /Nested Do Q")
	outer.Dict["Resources"] = types.Dict{"XObject": types.Dict{"Nested": *reference}}
	if err := outer.Encode(); err != nil {
		t.Fatal(err)
	}
	outerReference, err := doc.IndRefForNewObject(outer)
	if err != nil {
		t.Fatal(err)
	}
	page.DictEntry("Resources").DictEntry("XObject")["Form"] = *outerReference
	if _, err := ExternalizeInlineImages(t.Context(), doc); !errors.Is(err, model.ErrMaxRecursionDepthExceeded) {
		t.Fatalf("nested form limit not enforced: %v", err)
	}
}

func TestExternalizeInlineImagesInheritedResourcesAndMasks(t *testing.T) {
	doc, page := inlineTestDocument(t, "BI /W 9 /H 1 /IM true /D [1 0] ID \x00\x80 EI Q BI /W 1 /H 1 /BPC 8 /CS /Inherited ID x EI Q")
	root, err := doc.Pages()
	if err != nil {
		t.Fatal(err)
	}
	pages, err := doc.DereferenceDict(*root)
	if err != nil {
		t.Fatal(err)
	}
	resources := types.Dict{"ColorSpace": types.Dict{"Inherited": types.Name(model.DeviceGrayCS)}}
	pages["Resources"] = resources
	delete(page, "Resources")
	count, err := ExternalizeInlineImages(t.Context(), doc)
	if err != nil || count != 2 {
		t.Fatalf("count=%d, err=%v", count, err)
	}
	if resources["XObject"] != nil {
		t.Fatal("inherited resources changed")
	}
	objects := page.DictEntry("Resources").DictEntry("XObject")
	maskFound := false
	for _, reference := range objects {
		stream, _, err := doc.DereferenceStreamDict(reference)
		if err != nil {
			t.Fatal(err)
		}
		if mask := stream.BooleanEntry("ImageMask"); mask != nil && *mask {
			maskFound = bytes.Equal(stream.Raw, []byte{0, 128}) && reflect.DeepEqual(stream.ArrayEntry("Decode"), types.NewIntegerArray(1, 0))
		}
	}
	if !maskFound {
		t.Fatal("mask semantics lost")
	}
}

func TestExternalizeInlineImagesPreservesUncertainForms(t *testing.T) {
	for _, ownResources := range []bool{false, true} {
		t.Run(fmt.Sprint(ownResources), func(t *testing.T) {
			doc, page := inlineTestDocument(t, "/Form Do")
			form, err := doc.NewStreamDictForBuf([]byte("BI /W 1 /H 1 /BPC 8 /CS /G ID x EI Q"))
			if err != nil {
				t.Fatal(err)
			}
			form.Dict["Type"], form.Dict["Subtype"] = types.Name("XObject"), types.Name("Form")
			form.Dict["BBox"] = types.NewIntegerArray(0, 0, 10, 10)
			if err := form.Encode(); err != nil {
				t.Fatal(err)
			}
			if ownResources {
				form.Dict["Resources"] = types.NewDict()
				form.Raw = []byte("invalid compressed content")
				form.Content = nil
			}
			reference, err := doc.IndRefForNewObject(*form)
			if err != nil {
				t.Fatal(err)
			}
			page["Resources"] = types.Dict{"XObject": types.Dict{"Form": *reference}}
			before := len(doc.Table)
			original := append([]byte(nil), form.Raw...)
			if count, err := ExternalizeInlineImages(t.Context(), doc); count != 0 || err != nil || len(doc.Table) != before {
				t.Fatalf("count=%d, err=%v", count, err)
			}
			stored, _, err := doc.DereferenceStreamDict(*reference)
			if err != nil || !bytes.Equal(stored.Raw, original) {
				t.Fatalf("uncertain form changed: %v", err)
			}
		})
	}
}

func TestExternalizeInlineImagesPropagatesFormSourceFailure(t *testing.T) {
	doc, page := inlineTestDocument(t, "/Form Do")
	path := filepath.Join(t.TempDir(), "form.stream")
	if err := os.WriteFile(path, []byte("q Q"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := types.NewFileStreamSource(path)
	if err != nil {
		t.Fatal(err)
	}
	form := types.StreamDict{Dict: types.Dict{
		"Type": types.Name("XObject"), "Subtype": types.Name("Form"),
		"BBox": types.NewIntegerArray(0, 0, 10, 10), "Resources": types.NewDict(),
	}}
	if err := form.SetRawSource(source); err != nil {
		t.Fatal(err)
	}
	reference, err := doc.IndRefForNewObject(form)
	if err != nil {
		t.Fatal(err)
	}
	page["Resources"] = types.Dict{"XObject": types.Dict{"Form": *reference}}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := ExternalizeInlineImages(t.Context(), doc); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source failure lost: %v", err)
	}
}

func BenchmarkExternalizeInlineImageSpans(b *testing.B) {
	for _, size := range []int{1, 1000} {
		content := []byte(strings.Repeat("BI /W 1 /H 1 /BPC 8 /CS /G ID x EI Q ", size))
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			doc, err := CreateContextWithXRefTable(model.NewDefaultConfiguration(), &types.Dim{Width: 100, Height: 100})
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(content)))
			b.ResetTimer()
			for range b.N {
				if analysis, err := scanInlineImages(b.Context(), doc, content, nil); err != nil || len(analysis.images) != size {
					b.Fatalf("count=%d, err=%v", len(analysis.images), err)
				}
			}
		})
	}
}

func BenchmarkExternalizeInlineImagesSharedResources(b *testing.B) {
	for _, pageCount := range []int{1, 100} {
		b.Run(fmt.Sprint(pageCount), func(b *testing.B) {
			doc, err := CreateContextWithXRefTable(model.NewDefaultConfiguration(), &types.Dim{Width: 100, Height: 100})
			if err != nil {
				b.Fatal(err)
			}
			root, err := doc.Pages()
			if err != nil {
				b.Fatal(err)
			}
			pages, err := doc.DereferenceDict(*root)
			if err != nil {
				b.Fatal(err)
			}
			objects := types.NewDict()
			for index := range 1000 {
				reference, err := doc.StreamDictIndRef([]byte("image"))
				if err != nil {
					b.Fatal(err)
				}
				objects[fmt.Sprintf("Image%d", index)] = *reference
			}
			resources, err := doc.IndRefForNewObject(types.Dict{"XObject": objects})
			if err != nil {
				b.Fatal(err)
			}
			content, err := doc.StreamDictIndRef([]byte("q Q"))
			if err != nil {
				b.Fatal(err)
			}
			for range pageCount {
				reference, err := doc.EmptyPage(root, types.RectForFormat("A4"), 0)
				if err != nil {
					b.Fatal(err)
				}
				if err := model.AppendPageTree(reference, 1, pages); err != nil {
					b.Fatal(err)
				}
				page, err := doc.DereferenceDict(*reference)
				if err != nil {
					b.Fatal(err)
				}
				page["Resources"], page["Contents"] = *resources, *content
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if count, err := ExternalizeInlineImages(b.Context(), doc); count != 0 || err != nil {
					b.Fatalf("count=%d, err=%v", count, err)
				}
			}
		})
	}
}

func TestExternalizeInlineImagesWhitespaceSamplesAndResourceCollision(t *testing.T) {
	for _, separator := range []string{" ", "\r\n"} {
		samples := " \n\r\x00\t\xff"
		doc, page := inlineTestDocument(t, "BI /W 6 /H 1 /BPC 8 /CS /G ID"+separator+samples+" EI Q")
		name := fmt.Sprintf("Inline%d", *doc.Size)
		existing := types.Dict{name: types.Name("keep")}
		page["Resources"] = types.Dict{"XObject": existing}
		count, err := ExternalizeInlineImages(t.Context(), doc)
		if err != nil || count != 1 {
			t.Fatalf("count=%d, err=%v", count, err)
		}
		objects := page.DictEntry("Resources").DictEntry("XObject")
		if objects[name] != types.Name("keep") || len(existing) != 1 {
			t.Fatal("resource name collision replaced existing object")
		}
		stream, _, err := doc.DereferenceStreamDict(objects[name+"_1"])
		if err != nil || stream == nil || string(stream.Raw) != samples {
			t.Fatalf("whitespace samples changed: %v", err)
		}
	}
}

func FuzzInlineImageSpans(f *testing.F) {
	for _, seed := range []string{
		"BI /W 1 /H 1 /BPC 8 /CS /G ID x EI Q",
		"BI /W 1 /H 1 /BPC 8 /CS /G /F /Fl ID broken EI Q",
		"/Span <</ActualText (BI ID EI)>> BDC EMC",
		"BI /W 8 /H 1 /IM true ID \x80 EI /Next Do",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, content []byte) {
		if len(content) > 64<<10 {
			return
		}
		doc := sourceTestContext(t)
		doc.Limits.MaxDecodeBytes = 4096
		analysis, err := scanInlineImages(t.Context(), doc, content, nil)
		if err != nil {
			return
		}
		previous := 0
		for _, span := range analysis.images {
			if span.start < previous || span.end <= span.start || span.end > len(content) || !bytes.HasPrefix(content[span.start:], []byte("BI")) {
				t.Fatalf("invalid span %+v", span)
			}
			previous = span.end
		}
	})
}
