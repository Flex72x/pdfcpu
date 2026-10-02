package types

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/filter"
)

func TestFileStreamSourceWritesWithoutPayloadAllocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stream")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	err = errors.Join(file.Truncate(128<<20), file.Close())
	if err != nil {
		t.Fatal(err)
	}
	source, err := NewFileStreamSource(path)
	if err != nil {
		t.Fatal(err)
	}
	sd := StreamDict{Dict: Dict{}, Raw: []byte("old"), Content: []byte("stale")}
	if err := sd.SetRawSource(source); err != nil {
		t.Fatal(err)
	}
	if sd.Raw != nil || sd.Content != nil || sd.RawLength() != 128<<20 {
		t.Fatal("stream buffers retained")
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	n, err := sd.WriteRawTo(io.Discard)
	runtime.ReadMemStats(&after)
	if err != nil || n != 128<<20 {
		t.Fatalf("write: %d %v", n, err)
	}
	if after.TotalAlloc-before.TotalAlloc > 4<<20 {
		t.Fatalf("source buffered: %d", after.TotalAlloc-before.TotalAlloc)
	}
	clone := sd.Clone().(StreamDict)
	if clone.RawSource != sd.RawSource {
		t.Fatal("clone did not borrow immutable source")
	}
	if err := clone.Encode(); err != nil || clone.Raw != nil {
		t.Fatalf("Encode replaced opaque source: %v", err)
	}
	if _, err := clone.RawBytes(1 << 20); !errors.Is(err, filter.ErrDecodeLimitExceeded) {
		t.Fatalf("missing byte boundary limit: %v", err)
	}
	if err := os.Truncate(path, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := clone.WriteRawTo(io.Discard); err == nil {
		t.Fatal("changed source length accepted")
	}
}

type streamSourceTestReader struct {
	*bytes.Reader
	closeErr error
	closed   bool
}

func (r *streamSourceTestReader) Close() error { r.closed = true; return r.closeErr }

type streamSourceTestProvider struct {
	reader *streamSourceTestReader
	size   int64
}

func (s streamSourceTestProvider) Size() int64                  { return s.size }
func (s streamSourceTestProvider) Open() (io.ReadCloser, error) { return s.reader, nil }

type streamSourceTestWriter struct{ err error }

func (w streamSourceTestWriter) Write([]byte) (int, error) { return 0, w.err }

func TestEncodedStreamSourceRejectsLengthCloseAndSinkFailures(t *testing.T) {
	closeErr := errors.New("close failed")
	sinkErr := errors.New("sink failed")
	for _, test := range []struct {
		name     string
		size     int64
		closeErr error
		writer   io.Writer
	}{
		{"short", 4, nil, io.Discard},
		{"excess", 2, nil, io.Discard},
		{"close", 3, closeErr, io.Discard},
		{"sink", 3, nil, streamSourceTestWriter{err: sinkErr}},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &streamSourceTestReader{Reader: bytes.NewReader([]byte("abc")), closeErr: test.closeErr}
			sd := StreamDict{Dict: Dict{}}
			if err := sd.SetRawSource(streamSourceTestProvider{reader: reader, size: test.size}); err != nil {
				t.Fatal(err)
			}
			_, err := sd.WriteRawTo(test.writer)
			if err == nil || !reader.closed {
				t.Fatalf("failure/cleanup lost: %v closed=%v", err, reader.closed)
			}
			if test.name == "close" && !errors.Is(err, closeErr) {
				t.Fatal(err)
			}
			if test.name == "sink" && !errors.Is(err, sinkErr) {
				t.Fatal(err)
			}
		})
	}
}
