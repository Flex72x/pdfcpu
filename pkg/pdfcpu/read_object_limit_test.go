package pdfcpu

import (
	"context"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// A 200-byte file names object 2147483647. Everything sized by the highest object
// number must not follow it.
func TestReadDropsObjectsAboveLimit(t *testing.T) {
	const source = "%PDF-1.7\n" +
		"1 0 obj <</Type /Catalog /Pages 2 0 R>>\nendobj\n" +
		"2 0 obj <</Type /Pages /Kids [3 0 R] /Count 1>>\nendobj\n" +
		"3 0 obj <</Type /Page /Parent 2 0 R /MediaBox [0 0 10 10]>>\nendobj\n\n" +
		"2147483647 0 obj <</Root 1 0 R>>\nendobj\n"
	ctx, err := Read(context.Background(), strings.NewReader(source), model.NewDefaultConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	if _, found := ctx.Table[2147483647]; found {
		t.Fatal("object above the PDF limit was kept")
	}
	if *ctx.Size > maxPDFObjectNumber+1 || ctx.MaxObjNr > maxPDFObjectNumber {
		t.Fatalf("Size=%d MaxObjNr=%d", *ctx.Size, ctx.MaxObjNr)
	}
}
