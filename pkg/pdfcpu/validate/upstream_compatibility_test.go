package validate

import (
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestAnnotationDashPatternAcceptsLongArrays(t *testing.T) {
	xref := validationOrderXRefTable()
	for _, length := range []int{0, 1, 3, 4, 32} {
		pattern := make(types.Array, length)
		for index := range pattern {
			pattern[index] = types.Integer(index + 1)
		}
		if !validateDashPatternArray(xref, pattern) {
			t.Fatalf("valid dash pattern with %d elements was rejected", length)
		}
	}
	for _, pattern := range []types.Array{{types.Integer(-1)}, {types.Integer(0)}, {types.Name("bad")}} {
		if validateDashPatternArray(xref, pattern) {
			t.Fatalf("invalid dash pattern accepted: %v", pattern)
		}
	}
}

func TestPDF20AssociatedFileRelationshipsAcceptCustomNames(t *testing.T) {
	for _, mode := range []int{model.ValidationStrict, model.ValidationRelaxed} {
		xref := validationOrderXRefTable()
		version := model.V20
		xref.HeaderVersion, xref.ValidationMode = &version, mode
		for _, relationship := range []string{"Source", "Data", "Flex_Custom", "Flex:Custom"} {
			dict := types.Dict{"AFRelationship": types.Name(relationship)}
			if err := validateFileSpecDictPart2(xref, dict, 0, "fileSpecDict"); err != nil {
				t.Fatalf("mode=%d relationship=%s: %v", mode, relationship, err)
			}
		}
	}
}
