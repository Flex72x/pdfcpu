package types

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestEqualRawMemoryAndFileStreams(t *testing.T) {
	samples := bytes.Repeat([]byte("encoded stream samples"), 1<<15)
	different := bytes.Clone(samples)
	different[len(different)-1] ^= 1
	for _, test := range []struct {
		name string
		data []byte
		want bool
	}{
		{"same", samples, true},
		{"different", different, false},
		{"different-length", samples[:len(samples)-1], false},
	} {
		t.Run(test.name, func(t *testing.T) {
			leftPath, rightPath := filepath.Join(t.TempDir(), "left"), filepath.Join(t.TempDir(), "right")
			if err := os.WriteFile(leftPath, samples, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(rightPath, test.data, 0o600); err != nil {
				t.Fatal(err)
			}
			leftSource, err := NewFileStreamSource(leftPath)
			if err != nil {
				t.Fatal(err)
			}
			rightSource, err := NewFileStreamSource(rightPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, left := range []StreamDict{{Raw: samples}, {RawSource: leftSource}} {
				for _, right := range []StreamDict{{Raw: test.data}, {RawSource: rightSource}} {
					equal, err := left.EqualRaw(t.Context(), right)
					if err != nil || equal != test.want {
						t.Fatalf("left-backed=%t right-backed=%t equal=%t want=%t err=%v", left.RawSource != nil, right.RawSource != nil, equal, test.want, err)
					}
				}
			}
		})
	}
	for _, left := range []StreamDict{{Raw: []byte{}}, {RawSource: &decodeContextSource{}}} {
		for _, right := range []StreamDict{{Raw: []byte{}}, {RawSource: &decodeContextSource{}}} {
			if equal, err := left.EqualRaw(t.Context(), right); !equal || err != nil {
				t.Fatalf("empty streams differ: equal=%t err=%v", equal, err)
			}
		}
	}
}

func TestEqualRawSourceFailuresAndCancellation(t *testing.T) {
	for _, phase := range []string{"open", "read", "close", "cancel-open", "cancel-read"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cause := errors.New("source failed")
			left, right := &decodeContextSource{data: []byte("samples")}, &decodeContextSource{data: []byte("samples")}
			switch phase {
			case "open":
				right.openErr = cause
			case "read":
				right.readErr = cause
			case "close":
				right.closeErr = cause
			case "cancel-open":
				right.onOpen = cancel
			case "cancel-read":
				right.onRead = cancel
			}
			equal, err := (StreamDict{RawSource: left}).EqualRaw(ctx, StreamDict{RawSource: right})
			if equal || left.closed != 1 || right.closed != 1 {
				t.Fatalf("equal=%t err=%v closed=(%d,%d)", equal, err, left.closed, right.closed)
			}
			if phase == "cancel-open" || phase == "cancel-read" {
				if !errors.Is(err, context.Canceled) || errors.Is(err, ErrEncodedStreamSource) {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrEncodedStreamSource) || !errors.Is(err, cause) {
				t.Fatal(err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if equal, err := (StreamDict{Raw: []byte("same")}).EqualRaw(ctx, StreamDict{Raw: []byte("same")}); equal || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancel: equal=%t err=%v", equal, err)
	}
}

type equalRawDataErrorSource struct {
	data []byte
	err  error
}

func (source equalRawDataErrorSource) Size() int64 { return int64(len(source.data)) }
func (source equalRawDataErrorSource) Open() (io.ReadCloser, error) {
	return equalRawDataErrorReader{ReadCloser: io.NopCloser(bytes.NewReader(source.data)), err: source.err}, nil
}

type equalRawDataErrorReader struct {
	io.ReadCloser
	err error
}

func (reader equalRawDataErrorReader) Read(data []byte) (int, error) {
	count, err := reader.ReadCloser.Read(data)
	return count, errors.Join(err, reader.err)
}

func TestEqualRawPreservesDataAndErrorFromSource(t *testing.T) {
	cause := errors.New("source failed with data")
	data := []byte("equal samples")
	equal, err := (StreamDict{RawSource: equalRawDataErrorSource{data: data, err: cause}}).EqualRaw(t.Context(), StreamDict{Raw: data})
	if equal || !errors.Is(err, ErrEncodedStreamSource) || !errors.Is(err, cause) {
		t.Fatalf("equal=%t err=%v", equal, err)
	}
}
