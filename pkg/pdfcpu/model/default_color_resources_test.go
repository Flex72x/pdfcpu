package model

import (
	"fmt"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestConsolidateResourcesPreservesImplicitDefaultColorSpaces(t *testing.T) {
	for _, mode := range []int{ValidationStrict, ValidationRelaxed} {
		for _, content := range []string{".5 g .1 .2 .3 rg .1 .2 .3 .4 k /Im0 Do", "/Explicit cs /Im0 Do", "/DefaultRGB cs"} {
			t.Run(fmt.Sprintf("%d/%s", mode, content), func(t *testing.T) {
				colors := types.Dict{
					"DefaultGray": types.Name("DeviceGray"),
					"DefaultRGB":  types.Name("DeviceRGB"),
					"DefaultCMYK": types.Name("DeviceCMYK"),
					"Explicit":    types.Name("DeviceRGB"),
					"Unused":      types.Name("DeviceGray"),
				}
				resources := types.Dict{"ColorSpace": colors}
				names, err := parseContent(content)
				if err != nil {
					t.Fatal(err)
				}
				table := &XRefTable{ValidationMode: mode}
				if err := table.consolidateResourceSubDict(resources, "ColorSpace", names, 1); err != nil {
					t.Fatal(err)
				}
				actual := resources.DictEntry("ColorSpace")
				for _, name := range []string{"DefaultGray", "DefaultRGB", "DefaultCMYK"} {
					if actual[name] == nil {
						t.Fatalf("implicit %s removed", name)
					}
				}
				if actual["Unused"] != nil {
					t.Fatal("unused explicit color retained")
				}
				if names.Resources("ColorSpace")["Explicit"] && actual["Explicit"] == nil {
					t.Fatal("used explicit color removed")
				}
			})
		}
	}
}

func TestConsolidateResourcesRejectsMalformedColorDictionary(t *testing.T) {
	table := &XRefTable{ValidationMode: ValidationRelaxed}
	resources := types.Dict{"ColorSpace": types.Name("invalid")}
	if err := table.consolidateResourceSubDict(resources, "ColorSpace", NewPageResourceNames(), 1); err == nil {
		t.Fatal("malformed color resource dictionary accepted")
	}
}
