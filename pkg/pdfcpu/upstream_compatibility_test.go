package pdfcpu

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestOptimizationRejectsReferencedMalformedContent(t *testing.T) {
	for _, parameters := range []types.Array{{nil, nil}, {nil, types.Dict{"Quality": types.Integer(65)}}} {
		doc, page := inlineTestDocument(t, "q 0 0 20 20 re f Q")
		reference := page.ArrayEntry("Contents")[0].(types.IndirectRef)
		stream, _, err := doc.DereferenceStreamDict(reference)
		if err != nil {
			t.Fatal(err)
		}
		stream.Update("DecodeParms", parameters)
		var encoded bytes.Buffer
		doc.Write.Writer = bufio.NewWriter(&encoded)
		if err := WriteContext(t.Context(), doc); err != nil {
			t.Fatal(err)
		}
		read, err := Read(t.Context(), bytes.NewReader(encoded.Bytes()), model.NewStatelessConfiguration())
		if err == nil {
			err = OptimizeXRefTable(t.Context(), read)
		}
		if !errors.Is(err, errCorruptDecodeParms) && !errors.Is(err, errCorruptStreamDict) {
			t.Fatalf("referenced malformed content must fail rather than disappear: %v", err)
		}
	}
}

func TestContentStreamDeduplicationPreservesFileSources(t *testing.T) {
	for _, sameDictionary := range []bool{false, true} {
		for _, fileBacked := range []bool{false, true} {
			ctx := testOptimizeContext(t)
			original := optimizeTestStream(types.Dict{"Filter": types.Name("ASCIIHexDecode")}, "00>")
			candidate := optimizeTestStream(nil, "00>")
			if sameDictionary {
				candidate.Dict = original.Dict.Clone().(types.Dict)
			}
			path := filepath.Join(t.TempDir(), "encoded")
			if err := os.WriteFile(path, original.Raw, 0o600); err != nil {
				t.Fatal(err)
			}
			source, err := types.NewFileStreamSource(path)
			if err != nil {
				t.Fatal(err)
			}
			original.Raw, original.RawSource = nil, source
			if fileBacked {
				candidate.Raw, candidate.RawSource = nil, source
			}
			ctx.Optimize.ContentStreamCache[100] = original
			ctx.Table[100] = model.NewXRefTableEntryGen0(*original)
			generation := 7
			ctx.Table[100].Generation = &generation
			reference, err := optimizeContentStreamUsage(t.Context(), ctx, candidate, 101)
			if err != nil {
				t.Fatal(err)
			}
			if (reference != nil) != sameDictionary {
				t.Fatalf("same dictionary=%t, file-backed=%t: reference=%v", sameDictionary, fileBacked, reference)
			}
			if reference != nil && reference.GenerationNumber.Value() != generation {
				t.Fatalf("retained generation=%d, want %d", reference.GenerationNumber.Value(), generation)
			}
		}
	}
}

func TestCryptFilterByteLengthsPreserveLegacyAcceptance(t *testing.T) {
	for _, relaxed := range []bool{false, true} {
		for _, length := range []int{5, 16, 40, 128} {
			if _, err := checkCFLengthV2(length, false, relaxed); err != nil {
				t.Fatalf("V2 length=%d relaxed=%t: %v", length, relaxed, err)
			}
		}
		for _, length := range []int{16, 128} {
			if _, err := checkCFLengthAESV2(length, false, relaxed); err != nil {
				t.Fatalf("AESV2 length=%d relaxed=%t: %v", length, relaxed, err)
			}
		}
	}
	for _, keyLength := range []int{128, 256} {
		dict := newEncryptDict(false, true, keyLength, -4)
		if length := dict.DictEntry("CF").DictEntry("StdCF").IntEntry("Length"); length == nil || *length != keyLength/8 {
			t.Fatalf("crypt-filter length=%v, want %d bytes", length, keyLength/8)
		}
	}
}
