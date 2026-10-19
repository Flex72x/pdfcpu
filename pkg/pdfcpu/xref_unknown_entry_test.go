package pdfcpu

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func assertUnknownXRefEntry(t *testing.T, entry model.XRefTableEntry) {
	t.Helper()
	if !entry.Free || entry.Compressed || entry.Object != nil || entry.Incr != 2 {
		t.Fatalf("unexpected null entry: %+v", entry)
	}
	if entry.Offset == nil || *entry.Offset != 0 || entry.Generation == nil || *entry.Generation != 0 {
		t.Fatal("unknown entry must have initialized zero offset and generation")
	}
}

// TestXRefStreamUnknownEntryTypes verifies null resolution and safe free-list state for unknown types.
func TestXRefStreamUnknownEntryTypes(t *testing.T) {
	for _, entryType := range []int64{3, 255, 256, math.MaxInt64, -1} {
		t.Run(fmt.Sprint(entryType), func(t *testing.T) {
			ctx, err := model.NewContext(bytes.NewReader(nil), nil)
			if err != nil {
				t.Fatal(err)
			}
			entry := createXRefTableEntryFromXRefStream(entryType, 7, 123, 456, 789, ctx.Read.ObjectStreams, 2)
			assertUnknownXRefEntry(t, entry)
			if len(ctx.Read.ObjectStreams) != 0 {
				t.Fatal("unknown entry registered an object stream")
			}
			ctx.Table[7] = &entry
			obj, err := ctx.Dereference(*types.NewIndirectRef(7, 0))
			if err != nil || obj != nil {
				t.Fatalf("dereference = %v, %v; want null", obj, err)
			}
			// An older in-use entry must not replace the newer null entry.
			xsd := &types.XRefStreamDict{Objects: []int{7}, W: [3]int{1, 1, 1}}
			if err := extractXRefTableEntriesFromXRefStream([]byte{1, 42, 0}, 0, xsd, ctx, 3); err != nil {
				t.Fatal(err)
			}
			if ctx.Table[7] != &entry || !ctx.Table[7].Free {
				t.Fatal("older entry replaced the null entry")
			}
			if err := ctx.EnsureValidFreeList(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func xrefStreamUnknownTypePDF(version string, objNr, entryType int, encrypt bool) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "%%PDF-%s\n", version)
	offsets := make([]int, 5)
	for i, body := range []string{"<</Type/Catalog/Pages 2 0 R>>", "<</Type/Pages/Kids[]/Count 0>>"} {
		offsets[i+1] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	offsets[4] = b.Len()
	rows := [][3]int{{0, 0, 65535}, {1, offsets[1], 0}, {1, offsets[2], 0}, {0, 0, 0}, {1, offsets[4], 0}}
	rows[objNr] = [3]int{entryType, 123456, 42}
	var data bytes.Buffer
	for _, row := range rows {
		data.WriteByte(byte(row[0]))
		data.Write([]byte{byte(row[1] >> 24), byte(row[1] >> 16), byte(row[1] >> 8), byte(row[1])})
		data.Write([]byte{byte(row[2] >> 8), byte(row[2])})
	}
	extra := ""
	if encrypt {
		extra = "/Encrypt 3 0 R/ID[<00><00>]"
	}
	fmt.Fprintf(&b, "4 0 obj\n<</Type/XRef/Size 5/W[1 4 2]/Root 1 0 R%s/Length %d>>\nstream\n", extra, data.Len())
	b.Write(data.Bytes())
	b.WriteString("\nendstream\nendobj\n")
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", offsets[4])
	return b.Bytes()
}

// TestReadXRefStreamUnknownEntryType checks the reported panic paths and an unreferenced unknown entry.
func TestReadXRefStreamUnknownEntryType(t *testing.T) {
	for _, version := range []string{"1.5", "1.7", "2.0"} {
		for _, mode := range []int{model.ValidationStrict, model.ValidationRelaxed} {
			for _, tt := range []struct {
				name    string
				objNr   int
				encrypt bool
			}{
				{"free list head", 0, false},
				{"unreferenced", 3, false},
				{"encryption dictionary", 3, true},
			} {
				t.Run(fmt.Sprintf("%s/mode=%d/%s", version, mode, tt.name), func(t *testing.T) {
					conf := model.NewDefaultConfiguration()
					conf.ValidationMode = mode
					pdf := xrefStreamUnknownTypePDF(version, tt.objNr, 255, tt.encrypt)
					ctx, err := Read(t.Context(), bytes.NewReader(pdf), conf)
					if tt.encrypt {
						if !errors.Is(err, ErrMalformedEncryption) {
							t.Fatalf("got %v, want malformed encryption error", err)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					obj, err := ctx.Dereference(*types.NewIndirectRef(tt.objNr, 0))
					if err != nil || obj != nil {
						t.Fatalf("dereference = %v, %v; want null", obj, err)
					}
				})
			}
		}
	}
}
