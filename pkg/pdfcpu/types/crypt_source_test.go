package types

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/filter"
)

func TestCryptPipelinePreservesFileBackedDecode(t *testing.T) {
	samples := bytes.Repeat([]byte("plain samples"), 100)
	for _, compressed := range []bool{false, true} {
		for _, fileBacked := range []bool{false, true} {
			t.Run(fmt.Sprintf("compressed=%t/file-backed=%t", compressed, fileBacked), func(t *testing.T) {
				stream := StreamDict{Dict: NewDict(), Content: samples, FilterPipeline: []PDFFilter{{Name: "Crypt"}}}
				if compressed {
					stream.FilterPipeline = append(stream.FilterPipeline, PDFFilter{Name: filter.Flate})
				}
				if err := stream.Encode(); err != nil {
					t.Fatal(err)
				}
				stream.Content = nil
				if fileBacked {
					path := filepath.Join(t.TempDir(), "encoded")
					if err := os.WriteFile(path, stream.Raw, 0o600); err != nil {
						t.Fatal(err)
					}
					source, err := NewFileStreamSource(path)
					if err != nil {
						t.Fatal(err)
					}
					stream.Raw, stream.RawSource = nil, source
				}
				var decoded bytes.Buffer
				count, err := stream.WriteDecodedTo(t.Context(), &decoded, 1<<20)
				if err != nil || count != int64(len(samples)) || !bytes.Equal(decoded.Bytes(), samples) {
					t.Fatalf("stream decode count=%d, err=%v", count, err)
				}
				if err := stream.DecodeWithLimit(1 << 20); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(stream.Content, samples) {
					t.Fatal("buffered decode differs from original samples")
				}
			})
		}
	}
}
