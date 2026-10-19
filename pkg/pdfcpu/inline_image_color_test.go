package pdfcpu

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestExternalizeInlineImagesMaterializesDefaultColorSpaces(t *testing.T) {
	for _, test := range []struct {
		name, device, alias, key string
		components               int
	}{
		{"gray", model.DeviceGrayCS, "G", "DefaultGray", 1},
		{"rgb", model.DeviceRGBCS, "RGB", "DefaultRGB", 3},
		{"cmyk", model.DeviceCMYKCS, "CMYK", "DefaultCMYK", 4},
	} {
		for _, layout := range []string{"alias", "device", "named", "indexed", "named-indexed"} {
			t.Run(fmt.Sprintf("%s/%s", test.name, layout), func(t *testing.T) {
				indexed := layout == "indexed" || layout == "named-indexed"
				color := "/" + test.alias
				if layout == "device" {
					color = "/" + test.device
				}
				if layout == "named" || layout == "named-indexed" {
					color = "/Named"
				}
				samples := string(make([]byte, test.components))
				if indexed {
					if layout == "indexed" {
						color = fmt.Sprintf("[/I /%s 0 <%0*x>]", test.alias, test.components*2, 0)
					}
					samples = "\x00"
				}
				doc, page := inlineTestDocument(t, "BI /W 1 /H 1 /BPC 8 /CS "+color+" ID "+samples+" EI Q")
				doc.Optimize = testOptimizeContext(t).Optimize
				profile, err := doc.NewStreamDictForBuf([]byte("profile"))
				if err != nil {
					t.Fatal(err)
				}
				profile.Dict["N"] = types.Integer(test.components)
				profileReference, err := doc.IndRefForNewObject(*profile)
				if err != nil {
					t.Fatal(err)
				}
				space := types.Array{types.Name(model.ICCBasedCS), *profileReference}
				spaceReference, err := doc.IndRefForNewObject(space)
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
				spaces := types.Dict{test.key: *spaceReference, "Named": types.Name(test.device)}
				if layout == "named-indexed" {
					spaces["Named"] = types.Array{types.Name(model.IndexedCS), types.Name(test.device), types.Integer(0), types.HexLiteral(fmt.Sprintf("%0*x", test.components*2, 0))}
				}
				pages["Resources"] = types.Dict{"ColorSpace": spaces}
				delete(page, "Resources")
				if count, err := ExternalizeInlineImages(t.Context(), doc); err != nil || count != 1 {
					t.Fatalf("count=%d, err=%v", count, err)
				}
				if err := OptimizeXRefTable(t.Context(), doc); err != nil {
					t.Fatal(err)
				}
				objects := page.DictEntry("Resources").DictEntry("XObject")
				if len(objects) != 1 {
					t.Fatalf("image objects=%v", objects)
				}
				for _, reference := range objects {
					image, _, err := doc.DereferenceStreamDict(reference)
					if err != nil {
						t.Fatal(err)
					}
					actual := image.Dict["ColorSpace"]
					if indexed {
						actual = actual.(types.Array)[1]
					}
					if !reflect.DeepEqual(actual, *spaceReference) {
						t.Fatalf("effective color space=%v, want reference %v", actual, *spaceReference)
					}
				}
			})
		}
	}
}

func TestInlineColorSpaceRejectsUnusableDefaults(t *testing.T) {
	for _, color := range []types.Object{
		types.Name(model.DeviceGrayCS),
		types.Name("DefaultRGB"),
		types.Array{types.Name(model.IndexedCS), types.Name(model.DeviceRGBCS), types.Integer(0), types.HexLiteral("000000")},
		types.Array{types.Name(model.LabCS), types.Dict{}},
		types.Array{types.Name(model.PatternCS)},
	} {
		doc := sourceTestContext(t)
		resources := types.Dict{"ColorSpace": types.Dict{"DefaultRGB": color}}
		if _, err := inlineColorSpace(doc, types.Name("RGB"), resources); !errors.Is(err, errInlineImagePreserved) {
			t.Fatalf("invalid default=%v, err=%v", color, err)
		}
	}
}

func TestInlineColorSpaceRejectsReferenceCycles(t *testing.T) {
	doc := sourceTestContext(t)
	doc.Conf.Limits.MaxRecursionDepth = 8
	spaces := types.Dict{"Loop": types.Name("Loop")}
	resources := types.Dict{"ColorSpace": spaces}
	if _, err := inlineColorSpace(doc, types.Name("Loop"), resources); !errors.Is(err, errInlineImagePreserved) {
		t.Fatalf("named cycle accepted: %v", err)
	}
	reference, err := doc.IndRefForNewObject(types.Name("RGB"))
	if err != nil {
		t.Fatal(err)
	}
	doc.Table[reference.ObjectNumber.Value()].Object = *reference
	spaces["DefaultRGB"] = *reference
	if _, err := inlineColorSpace(doc, types.Name("RGB"), resources); !errors.Is(err, errInlineImagePreserved) {
		t.Fatalf("indirect default cycle accepted: %v", err)
	}
	spaces["DefaultRGB"] = types.Name(model.DeviceRGBCS)
	if actual, err := inlineColorSpace(doc, types.Name("RGB"), resources); err != nil || actual != types.Name(model.DeviceRGBCS) {
		t.Fatalf("identity default rejected: color=%v, err=%v", actual, err)
	}
}
