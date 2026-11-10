package pdfcpu_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// Relaxed validation accepts an indirect StructTreeRoot whose /Type is not a
// name; writing the accepted document must not reject it again.
func TestWriteAcceptsNonNameTypeInRelaxedMode(t *testing.T) {
	for _, mode := range []int{model.ValidationRelaxed, model.ValidationStrict} {
		conf := model.NewDefaultConfiguration()
		conf.ValidationMode = mode
		ctx, err := api.ReadContext(t.Context(), bytes.NewReader(nonNameTypeFixture()), conf)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err = api.Write(t.Context(), ctx, &out, conf)
		if mode == model.ValidationStrict {
			if err == nil || !strings.Contains(err.Error(), "Type") {
				t.Fatalf("strict: expected Type error, got %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("relaxed: %v", err)
		}
		if _, err = api.ReadContext(t.Context(), bytes.NewReader(out.Bytes()), conf); err != nil {
			t.Fatalf("relaxed readback: %v", err)
		}
	}
}

func nonNameTypeFixture() []byte {
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R /StructTreeRoot 5 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Resources <<>> /Contents 4 0 R >>",
		"<< /Length 0 >>\nstream\n\nendstream",
		"<< /Type 5 >>",
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
