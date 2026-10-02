package api

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func streamSourceDocument(t *testing.T) (*model.Context, int, []byte) {
	t.Helper()
	file := openAPITestPDF(t, "..", "testdata", "test.pdf")
	doc, err := ReadContext(t.Context(), file, model.NewDefaultConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	for number, entry := range doc.Table {
		sd, ok := entry.Object.(types.StreamDict)
		if !ok || len(sd.Raw) == 0 {
			continue
		}
		path := filepath.Join(t.TempDir(), "stream.bin")
		if err := os.WriteFile(path, sd.Raw, 0o600); err != nil {
			t.Fatal(err)
		}
		original := sd.Raw
		source, err := types.NewFileStreamSource(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := sd.SetRawSource(source); err != nil {
			t.Fatal(err)
		}
		entry.Object = sd
		return doc, number, original
	}
	t.Fatal("fixture has no stream")
	return nil, 0, nil
}

func TestFileBackedStreamWriterRoundTrip(t *testing.T) {
	doc, number, original := streamSourceDocument(t)
	var output bytes.Buffer
	if err := Write(t.Context(), doc, &output, doc.Configuration); err != nil {
		t.Fatal(err)
	}
	readback, err := ReadContext(t.Context(), bytes.NewReader(output.Bytes()), model.NewDefaultConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateContext(t.Context(), readback); err != nil {
		t.Fatal(err)
	}
	entry, ok := readback.FindTableEntry(number, 0)
	if !ok || !bytes.Equal(entry.Object.(types.StreamDict).Raw, original) {
		t.Fatal("file-backed stream changed encoded bytes")
	}
}

type cancelStreamSink struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func (w cancelStreamSink) Write(data []byte) (int, error) {
	w.cancel()
	return len(data), nil
}

func TestFileBackedStreamWriterPropagatesCancellationAndSinkFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	doc, _, _ := streamSourceDocument(t)
	if err := Write(ctx, doc, cancelStreamSink{ctx: ctx, cancel: cancel}, doc.Configuration); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	doc, _, _ = streamSourceDocument(t)
	sentinel := errors.New("sink failed")
	if err := Write(t.Context(), doc, failingWriter{err: sentinel}, doc.Configuration); !errors.Is(err, sentinel) {
		t.Fatalf("sink error lost: %v", err)
	}
}
