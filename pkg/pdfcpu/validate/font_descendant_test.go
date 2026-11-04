package validate

import (
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestRelaxedValidationKeepsFontWithQuestionableDescendant(t *testing.T) {
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed
	ctx, err := model.NewContext(strings.NewReader(""), conf)
	if err != nil {
		t.Fatal(err)
	}
	version := model.V17
	ctx.HeaderVersion = &version
	font := types.Dict{
		"Type":            types.Name("Font"),
		"Subtype":         types.Name("Type0"),
		"BaseFont":        types.Name("Test"),
		"Encoding":        types.Name("Identity-H"),
		"DescendantFonts": types.Array{types.Dict{"Type": types.Name("Font")}},
	}
	ctx.Table[7] = model.NewXRefTableEntryGen0(font)
	resources := types.Dict{"F1": *types.NewIndirectRef(7, 0)}
	if err := validateFontResources(t.Context(), ctx.XRefTable, resources, 0); err != nil {
		t.Fatal(err)
	}
	if resources["F1"] == nil {
		t.Fatal("the font reference was removed, the text of the page would be lost")
	}
}
