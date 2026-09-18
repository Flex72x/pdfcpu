package pdfcpu_test

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestWritePreservesMeasureDistanceFormats(t *testing.T) {
	for _, directMeasure := range []bool{false, true} {
		for _, indirectArray := range []bool{false, true} {
			for _, optionalTypes := range []bool{false, true} {
				for _, objectStreams := range []bool{false, true} {
					name := fmt.Sprintf("directMeasure=%t/indirectArray=%t/types=%t/objectStreams=%t", directMeasure, indirectArray, optionalTypes, objectStreams)
					t.Run(name, func(t *testing.T) {
						conf := model.NewDefaultConfiguration()
						conf.ValidationMode = model.ValidationRelaxed
						conf.WriteObjectStream = objectStreams
						source := measureWriteFixture(directMeasure, indirectArray, optionalTypes, "GoTo")
						before, err := api.ReadContext(bytes.NewReader(source), conf)
						if err != nil {
							t.Fatal(err)
						}
						if err = api.ValidateContext(before); err != nil {
							t.Fatal(err)
						}
						measure := pageMeasure(t, before)
						original := measure.Clone()
						formats, err := before.DereferenceArray(measure["D"])
						if err != nil {
							t.Fatal(err)
						}
						want := make([]types.Dict, len(formats))
						for i, ref := range formats {
							d, err := before.DereferenceDict(ref)
							if err != nil || d == nil {
								t.Fatalf("source D[%d]: %v", i, err)
							}
							want[i] = d.Clone().(types.Dict)
						}
						var out bytes.Buffer
						if err = api.Write(before, &out, conf); err != nil {
							t.Fatal(err)
						}
						if !reflect.DeepEqual(original, measure) {
							t.Fatal("writer changed the source Measure dictionary")
						}
						after, err := api.ReadContext(bytes.NewReader(out.Bytes()), conf)
						if err != nil {
							t.Fatal(err)
						}
						if err = api.ValidateContext(after); err != nil {
							t.Fatalf("readback validation: %v", err)
						}
						restored := pageMeasure(t, after)
						if !reflect.DeepEqual(original, restored) {
							t.Fatalf("Measure changed: %v => %v", original, restored)
						}
						got, err := after.DereferenceArray(restored["D"])
						if err != nil || len(got) != len(want) {
							t.Fatalf("distance array: %v, %v", got, err)
						}
						for i, ref := range got {
							if _, ok := ref.(types.IndirectRef); !ok {
								t.Fatalf("D[%d] lost its indirect reference", i)
							}
							d, err := after.DereferenceDict(ref)
							if err != nil || !reflect.DeepEqual(want[i], d) {
								t.Fatalf("D[%d] changed or missing: %v, %v", i, d, err)
							}
						}
						// An unknown extension reachable only through D[0] must survive too.
						extension, err := after.DereferenceDict(types.IndirectRef{ObjectNumber: 15, GenerationNumber: 0})
						if err != nil || extension == nil || extension.StringEntry("Note") == nil || *extension.StringEntry("Note") != "keep" {
							t.Fatalf("distance extension lost: %v, %v", extension, err)
						}
						assertMeasureFixtureDestinations(t, after)
					})
				}
			}
		}
	}
}

func TestWritePreservesGoToActionDestinations(t *testing.T) {
	for _, kind := range []string{"GoTo", "GoToR", "GoToE"} {
		t.Run(kind, func(t *testing.T) {
			conf := model.NewDefaultConfiguration()
			conf.ValidationMode = model.ValidationRelaxed
			source := measureWriteFixture(false, true, true, kind)
			doc, err := api.ReadContext(bytes.NewReader(source), conf)
			if err != nil {
				t.Fatal(err)
			}
			if err = api.ValidateContext(doc); err != nil {
				t.Fatal(err)
			}
			ref := *types.NewIndirectRef(16, 0)
			action, err := doc.DereferenceDict(ref)
			if err != nil || action == nil {
				t.Fatalf("source action: %v, %v", action, err)
			}
			want := action.Clone()
			var out bytes.Buffer
			if err = api.Write(doc, &out, conf); err != nil {
				t.Fatal(err)
			}
			after, err := api.ReadContext(bytes.NewReader(out.Bytes()), conf)
			if err != nil {
				t.Fatal(err)
			}
			if err = api.ValidateContext(after); err != nil {
				t.Fatal(err)
			}
			got, err := after.DereferenceDict(ref)
			if err != nil || !reflect.DeepEqual(want, got) {
				t.Fatalf("action changed: %v => %v, %v", want, got, err)
			}
			destination, err := after.DereferenceArray(got["D"])
			if err != nil || len(destination) != 2 || destination[1] != types.Name("Fit") {
				t.Fatalf("destination lost: %v, %v", destination, err)
			}
			if kind != "GoTo" && destination[0] != types.Integer(0) {
				t.Fatalf("remote page number changed: %v", destination)
			}
			if after.PageCount != 2 {
				t.Fatalf("page count changed: %d", after.PageCount)
			}
		})
	}
}

func pageMeasure(t *testing.T, doc *model.Context) types.Dict {
	t.Helper()
	page, _, _, err := doc.PageDict(1, false)
	if err != nil {
		t.Fatal(err)
	}
	viewports, err := doc.DereferenceArray(page["VP"])
	if err != nil || len(viewports) != 1 {
		t.Fatalf("viewports: %v, %v", viewports, err)
	}
	viewport, err := doc.DereferenceDict(viewports[0])
	if err != nil || viewport == nil {
		t.Fatalf("viewport: %v, %v", viewport, err)
	}
	measure, err := doc.DereferenceDict(viewport["Measure"])
	if err != nil || measure == nil {
		t.Fatalf("measure: %v, %v", measure, err)
	}
	return measure
}

func assertMeasureFixtureDestinations(t *testing.T, doc *model.Context) {
	t.Helper()
	if doc.PageCount != 2 {
		t.Fatalf("page count changed: %d", doc.PageCount)
	}
	for _, objNr := range []int{13, 14} {
		annotation, err := doc.DereferenceDict(*types.NewIndirectRef(objNr, 0))
		if err != nil || annotation == nil {
			t.Fatalf("annotation %d: %v, %v", objNr, annotation, err)
		}
		value := annotation["Dest"]
		if objNr == 13 {
			action, err := doc.DereferenceDict(annotation["A"])
			if err != nil || action == nil {
				t.Fatalf("action: %v, %v", action, err)
			}
			value = action["D"]
		}
		destination, err := doc.DereferenceArray(value)
		if err != nil || len(destination) != 2 || destination[0] != *types.NewIndirectRef(4, 0) || destination[1] != types.Name("Fit") {
			t.Fatalf("destination changed: %v, %v", destination, err)
		}
		page, err := doc.DereferenceDict(destination[0])
		if err != nil || page == nil || !page.IsPage() {
			t.Fatalf("destination page lost: %v, %v", page, err)
		}
	}
}

func measureWriteFixture(directMeasure, indirectArray, optionalTypes bool, actionKind string) []byte {
	distance := "[9 0 R 10 0 R]"
	if indirectArray {
		distance = "12 0 R"
	}
	kind := ""
	if optionalTypes {
		kind = "/Type /Measure /Subtype /RL "
	}
	measure := "<< " + kind + "/R (1:1) /X [8 0 R] /D " + distance + " /A [11 0 R] >>"
	measureRef := "7 0 R"
	if directMeasure {
		measureRef = measure
	}
	action := "<< /S 18 0 R /D 17 0 R >>"
	destination := "[4 0 R /Fit]"
	if actionKind == "GoToR" || actionKind == "GoToE" {
		action = "<< /S 18 0 R /F (other.pdf) /D 17 0 R >>"
		destination = "[0 /Fit]"
	}
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Resources <<>> /Contents 6 0 R /VP [5 0 R] /Annots [13 0 R 14 0 R] >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Resources <<>> /Contents 6 0 R >>",
		"<< /Type /Viewport /BBox [0 0 100 100] /Measure " + measureRef + " >>",
		"<< /Length 0 >>\nstream\n\nendstream",
		measure,
		"<< /U (mm) /C 0.35278 >>",
		"<< /U (mm) /C 1 /F /D /D 100 /FD true /Extension 15 0 R >>",
		"<< /U (m) /C 0.001 >>",
		"<< /U (mm2) /C 1 >>",
		"[9 0 R 10 0 R]",
		"<< /Type /Annot /Subtype /Link /Rect [0 0 10 10] /A 16 0 R >>",
		"<< /Type /Annot /Subtype /Link /Rect [20 0 30 10] /Dest [4 0 R /Fit] >>",
		"<< /Note (keep) >>",
		action,
		destination,
		"/" + actionKind,
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.7\n")
	offsets := make([]int, len(objects))
	for i, object := range objects {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return out.Bytes()
}
