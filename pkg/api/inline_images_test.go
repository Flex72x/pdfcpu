package api

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func inlineImageAPIPDF(t *testing.T) []byte {
	t.Helper()
	doc, err := pdfcpu.CreateContextWithXRefTable(model.NewStatelessConfiguration(), &types.Dim{Width: 100, Height: 100})
	if err != nil {
		t.Fatal(err)
	}
	root, err := doc.Pages()
	if err != nil {
		t.Fatal(err)
	}
	pages, err := doc.DereferenceDict(*root)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := doc.EmptyPage(root, types.NewRectangle(0, 0, 100, 100), 0)
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
	content, err := doc.StreamDictIndRef([]byte("q 100 0 0 100 0 0 cm BI /W 1 /H 1 /BPC 8 /CS /RGB ID \xff\x00\x00 EI Q"))
	if err != nil {
		t.Fatal(err)
	}
	page["Contents"] = *content
	doc.PageCount = 1
	var result bytes.Buffer
	if err := WriteContext(t.Context(), doc, &result); err != nil {
		t.Fatal(err)
	}
	return result.Bytes()
}

func TestInlineImagesSharedOptimizedRegistry(t *testing.T) {
	source := inlineImageAPIPDF(t)
	conf := model.NewStatelessConfiguration()
	conf.Optimize = false
	images, err := Images(t.Context(), bytes.NewReader(source), nil, conf)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || len(images[0]) != 1 {
		t.Fatalf("inline image missing from list: %+v", images)
	}
	var objectNumber int
	for number := range images[0] {
		objectNumber = number
	}
	extracted, err := ExtractImagesRaw(t.Context(), bytes.NewReader(source), nil, conf)
	if err != nil {
		t.Fatal(err)
	}
	if len(extracted) != 1 || len(extracted[0]) != 1 {
		t.Fatalf("inline image missing from extraction: %+v", extracted)
	}
	actual, err := png.Decode(extracted[0][objectNumber].Reader)
	if err != nil {
		t.Fatal(err)
	}
	if actual.Bounds().Dx() != 1 || actual.Bounds().Dy() != 1 || color.NRGBAModel.Convert(actual.At(0, 0)) != (color.NRGBA{R: 255, A: 255}) {
		t.Fatal("extracted inline samples changed")
	}
	replacement := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	replacement.SetNRGBA(0, 0, color.NRGBA{G: 255, A: 255})
	var encoded, output bytes.Buffer
	if err := png.Encode(&encoded, replacement); err != nil {
		t.Fatal(err)
	}
	if err := UpdateImages(t.Context(), bytes.NewReader(source), &encoded, &output, objectNumber, 0, "", conf); err != nil {
		t.Fatal(err)
	}
	updated, err := ExtractImagesRaw(t.Context(), bytes.NewReader(output.Bytes()), nil, conf)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 1 || len(updated[0]) != 1 {
		t.Fatalf("updated image missing: %+v", updated)
	}
	for _, value := range updated[0] {
		actual, err = png.Decode(value.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if color.NRGBAModel.Convert(actual.At(0, 0)) != (color.NRGBA{G: 255, A: 255}) {
			t.Fatal("inline image update did not replace samples")
		}
	}
}

func TestInlineImagesStructuralOptimizationGate(t *testing.T) {
	source := inlineImageAPIPDF(t)
	for _, optimize := range []bool{false, true} {
		conf := model.NewStatelessConfiguration()
		conf.Optimize = optimize
		var output bytes.Buffer
		if err := Rotate(t.Context(), bytes.NewReader(source), &output, 90, nil, conf); err != nil {
			t.Fatal(err)
		}
		doc, err := ReadAndValidate(t.Context(), bytes.NewReader(output.Bytes()), model.NewStatelessConfiguration())
		if err != nil {
			t.Fatal(err)
		}
		page, _, _, err := doc.PageDict(t.Context(), 1, false)
		if err != nil {
			t.Fatal(err)
		}
		content, err := doc.PageContent(page, 1)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(content, []byte("BI /W")) == optimize {
			t.Fatalf("Rotate Optimize=%t ignored normalization gate: %q", optimize, content)
		}
		if rotation := page.IntEntry("Rotate"); rotation == nil || *rotation != 90 {
			t.Fatal("rotation lost")
		}
	}
}
