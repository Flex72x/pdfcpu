package filter

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"testing"
)

func TestFlateDecodeToMatchesBoundedDecoder(t *testing.T) {
	for _, predictor := range []int{1, 2, 10, 11, 12, 13, 14, 15} {
		for _, filterByte := range []byte{0, 1, 2, 3, 4} {
			t.Run(fmt.Sprintf("%d/%d", predictor, filterByte), func(t *testing.T) {
				// Arbitrary compressed rows exercise all reconstructions; both decoders
				// must return exactly the same samples and errors for the same stream.
				input := make([]byte, 0, 19*3*11+11)
				for y := range 11 {
					if predictor >= 10 {
						input = append(input, filterByte)
					}
					for x := range 19 * 3 {
						input = append(input, byte(x*37+y*13))
					}
				}
				var encoded bytes.Buffer
				w := zlib.NewWriter(&encoded)
				if _, err := w.Write(input); err != nil {
					t.Fatal(err)
				}
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}
				parms := map[string]int{"Predictor": predictor, "Colors": 3, "Columns": 19, "BitsPerComponent": 8}
				f := flate{baseFilter{parms: parms, maxDecodeBytes: 1 << 20}}
				reader, err := f.Decode(bytes.NewReader(encoded.Bytes()))
				if err != nil {
					t.Fatal(err)
				}
				expected, err := io.ReadAll(reader)
				if err != nil {
					t.Fatal(err)
				}
				var actual bytes.Buffer
				n, err := DecodeTo(Flate, parms, 1<<20, bytes.NewReader(encoded.Bytes()), &actual)
				if err != nil || n != int64(len(expected)) || !bytes.Equal(actual.Bytes(), expected) {
					t.Fatalf("stream differs: %d %v", n, err)
				}
				if _, err = DecodeTo(Flate, parms, int64(len(expected))-1, bytes.NewReader(encoded.Bytes()), io.Discard); !errors.Is(err, ErrDecodeLimitExceeded) {
					t.Fatalf("limit not enforced: %v", err)
				}
			})
		}
	}
}

func TestFlateDecodeToPropagatesSinkAndChecksumFailures(t *testing.T) {
	data := bytes.Repeat([]byte("bounded samples"), 100)
	var encoded bytes.Buffer
	w := zlib.NewWriter(&encoded)
	_, _ = w.Write(data)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("sink failed")
	if _, err := DecodeTo(Flate, nil, 1<<20, bytes.NewReader(encoded.Bytes()), decodeToFailWriter{sentinel}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	damaged := bytes.Clone(encoded.Bytes())
	damaged[len(damaged)-1] ^= 1
	if _, err := DecodeTo(Flate, nil, 1<<20, bytes.NewReader(damaged), io.Discard); !errors.Is(err, zlib.ErrChecksum) {
		t.Fatal(err)
	}
	// A failed decode must not poison pooled zlib state used by the next request.
	var actual bytes.Buffer
	if _, err := DecodeTo(Flate, nil, 1<<20, bytes.NewReader(encoded.Bytes()), &actual); err != nil || !bytes.Equal(actual.Bytes(), data) {
		t.Fatal(err)
	}
}

type decodeToFailWriter struct{ err error }

func (w decodeToFailWriter) Write([]byte) (int, error) { return 0, w.err }
