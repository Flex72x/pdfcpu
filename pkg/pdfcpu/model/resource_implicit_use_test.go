package model

import (
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestConsolidationKeepsResourcesOfFormWithoutOwnResources(t *testing.T) {
	xRefTable := newXRefTable(NewDefaultConfiguration())
	image := types.Dict{"Subtype": types.Name("Image")}
	imageSD := types.NewStreamDict(image, 0, nil, nil, nil)
	form := types.Dict{"Subtype": types.Name("Form")}
	formSD := types.NewStreamDict(form, 0, nil, nil, nil)
	formWithOwn := types.Dict{"Subtype": types.Name("Form"), "Resources": types.Dict{}}
	formWithOwnSD := types.NewStreamDict(formWithOwn, 0, nil, nil, nil)
	xRefTable.Table[1] = NewXRefTableEntryGen0(imageSD)
	xRefTable.Table[2] = NewXRefTableEntryGen0(formSD)
	xRefTable.Table[3] = NewXRefTableEntryGen0(formWithOwnSD)
	ref := func(n int) types.IndirectRef { return *types.NewIndirectRef(n, 0) }

	inherited := types.Dict{"XObject": types.Dict{"Im": ref(1), "Tr": ref(2)}}
	if err := xRefTable.consolidateResourceDict(inherited, parseTestNames(t, "/Tr Do"), 1); err != nil {
		t.Fatal(err)
	}
	if _, found := inherited["XObject"].(types.Dict)["Im"]; !found {
		t.Fatal("the image a form inherits from the page was removed")
	}

	own := types.Dict{"XObject": types.Dict{"Im": ref(1), "Tr": ref(3)}}
	if err := xRefTable.consolidateResourceDict(own, parseTestNames(t, "/Tr Do"), 1); err != nil {
		t.Fatal(err)
	}
	if _, found := own["XObject"].(types.Dict)["Im"]; found {
		t.Fatal("an unused image stayed although every form has its own resources")
	}
}

func parseTestNames(t *testing.T, content string) PageResourceNames {
	t.Helper()
	names, err := parseContent(content)
	if err != nil {
		t.Fatal(err)
	}
	return names
}
