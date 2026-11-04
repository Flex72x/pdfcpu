package validate

import (
	"context"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func rootWithAssociatedFiles(t *testing.T, mode int) (*model.Context, types.Dict) {
	t.Helper()
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = mode
	ctx, err := model.NewContext(strings.NewReader(""), conf)
	if err != nil {
		t.Fatal(err)
	}
	version := model.V20
	ctx.HeaderVersion = &version
	return ctx, types.Dict{"AF": types.Array{types.Dict{}}}
}

func TestRelaxedValidationKeepsDocumentWithUnsupportedCatalogEntry(t *testing.T) {
	ctx, root := rootWithAssociatedFiles(t, model.ValidationRelaxed)
	if err := validateRootEntries(t.Context(), ctx.XRefTable, root, 1); err != nil {
		t.Fatal(err)
	}
	if root["AF"] == nil {
		t.Fatal("the entry must stay in the document")
	}
}

func TestStrictValidationReportsUnsupportedCatalogEntry(t *testing.T) {
	ctx, root := rootWithAssociatedFiles(t, model.ValidationStrict)
	if err := validateRootEntries(t.Context(), ctx.XRefTable, root, 1); err == nil {
		t.Fatal("strict validation accepted the entry")
	}
}

func TestRelaxedValidationStopsWhenCancelled(t *testing.T) {
	ctx, root := rootWithAssociatedFiles(t, model.ValidationRelaxed)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := validateRootEntries(cancelled, ctx.XRefTable, root, 1); err == nil {
		t.Fatal("cancellation was swallowed")
	}
}
