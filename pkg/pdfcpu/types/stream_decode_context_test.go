package types

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/ascii85"
	"errors"
	"io"
	"math"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/filter"
)

type decodeContextSource struct {
	data      []byte
	openErr   error
	readErr   error
	closeErr  error
	onOpen    func()
	onRead    func()
	reads     int
	closed    int
	readSize  int
	failAfter int
}

func (source *decodeContextSource) Size() int64 { return int64(len(source.data)) }

func (source *decodeContextSource) Open() (io.ReadCloser, error) {
	if source.onOpen != nil {
		source.onOpen()
	}
	return &decodeContextReader{source: source, Reader: bytes.NewReader(source.data)}, source.openErr
}

type decodeContextReader struct {
	*bytes.Reader
	source *decodeContextSource
}

type decodeCancelWriter struct {
	cancel context.CancelFunc
	count  int
}

func (writer *decodeCancelWriter) Write(data []byte) (int, error) {
	writer.count += len(data)
	writer.cancel()
	return len(data), nil
}

func (reader *decodeContextReader) Read(data []byte) (int, error) {
	reader.source.reads++
	if reader.source.onRead != nil {
		reader.source.onRead()
	}
	if reader.source.readErr != nil && reader.source.reads > reader.source.failAfter {
		return 0, reader.source.readErr
	}
	if reader.source.readSize > 0 && len(data) > reader.source.readSize {
		data = data[:reader.source.readSize]
	}
	return reader.Reader.Read(data)
}

func (reader *decodeContextReader) Close() error {
	reader.source.closed++
	return reader.source.closeErr
}

func streamContextFlate(t *testing.T, samples []byte) []byte {
	t.Helper()
	var encoded bytes.Buffer
	writer := zlib.NewWriter(&encoded)
	if _, err := writer.Write(samples); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func streamContextASCII85(t *testing.T, data []byte) []byte {
	t.Helper()
	var encoded bytes.Buffer
	writer := ascii85.NewEncoder(&encoded)
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	encoded.WriteString("~>")
	return encoded.Bytes()
}

func TestWriteDecodedToContextAndSourceFailures(t *testing.T) {
	samples := bytes.Repeat([]byte("row samples"), 1<<14)
	flate := streamContextFlate(t, samples)
	for _, pipeline := range []struct {
		name    string
		data    []byte
		filters []PDFFilter
	}{
		{"raw", samples, nil},
		{"flate", flate, []PDFFilter{{Name: filter.Flate}}},
		{"multi", streamContextASCII85(t, flate), []PDFFilter{{Name: filter.ASCII85}, {Name: filter.Flate}}},
	} {
		for _, phase := range []string{"open", "read"} {
			t.Run(pipeline.name+"/cancel/"+phase, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				source := &decodeContextSource{data: pipeline.data}
				if phase == "open" {
					source.onOpen = cancel
				} else {
					source.onRead = cancel
				}
				stream := StreamDict{Dict: NewDict(), FilterPipeline: pipeline.filters}
				if err := stream.SetRawSource(source); err != nil {
					t.Fatal(err)
				}
				count, err := stream.WriteDecodedTo(ctx, io.Discard, 1<<20)
				if !errors.Is(err, context.Canceled) || errors.Is(err, ErrEncodedStreamSource) || source.closed != 1 || source.reads > 1 || count != 0 {
					t.Fatalf("count=%d err=%v reads=%d closed=%d", count, err, source.reads, source.closed)
				}
			})
		}
		for _, phase := range []string{"open", "read", "close"} {
			t.Run(pipeline.name+"/failure/"+phase, func(t *testing.T) {
				failure := errors.New("source " + phase)
				source := &decodeContextSource{data: pipeline.data}
				switch phase {
				case "open":
					source.openErr = failure
				case "read":
					source.readErr = failure
				case "close":
					source.closeErr = failure
				}
				stream := StreamDict{Dict: NewDict(), FilterPipeline: pipeline.filters}
				if err := stream.SetRawSource(source); err != nil {
					t.Fatal(err)
				}
				_, err := stream.WriteDecodedTo(t.Context(), io.Discard, 1<<20)
				if !errors.Is(err, failure) || !errors.Is(err, ErrEncodedStreamSource) || source.closed != 1 {
					t.Fatalf("err=%v closed=%d", err, source.closed)
				}
			})
		}
		t.Run(pipeline.name+"/cancel-output-and-close-failure", func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			closeError := errors.New("source close")
			source := &decodeContextSource{data: pipeline.data, closeErr: closeError}
			stream := StreamDict{Dict: NewDict(), FilterPipeline: pipeline.filters}
			if err := stream.SetRawSource(source); err != nil {
				t.Fatal(err)
			}
			writer := &decodeCancelWriter{cancel: cancel}
			count, err := stream.WriteDecodedTo(ctx, writer, 1<<20)
			if !errors.Is(err, context.Canceled) || !errors.Is(err, closeError) || !errors.Is(err, ErrEncodedStreamSource) || source.closed != 1 || count <= 0 || count >= int64(len(samples)) || count != int64(writer.count) {
				t.Fatalf("count=%d err=%v writerCount=%d closed=%d", count, err, writer.count, source.closed)
			}
		})
	}
}

func TestWriteDecodedToPipelinePreservesSamplesPredictorAndCache(t *testing.T) {
	predicted := []byte{0, 11, 22, 33, 2, 1, 2, 3}
	expected := []byte{11, 22, 33, 12, 24, 36}
	stream := StreamDict{
		Dict: NewDict(), Raw: streamContextASCII85(t, streamContextFlate(t, predicted)), Content: []byte("stale cache"),
		FilterPipeline: []PDFFilter{{Name: filter.ASCII85}, {Name: filter.Flate, DecodeParms: Dict{
			"Predictor": Integer(15), "Colors": Integer(1), "BitsPerComponent": Integer(8), "Columns": Integer(3),
		}}},
	}
	var output bytes.Buffer
	count, err := stream.WriteDecodedTo(t.Context(), &output, 1024)
	if err != nil || count != int64(len(expected)) || !bytes.Equal(output.Bytes(), expected) || string(stream.Content) != "stale cache" {
		t.Fatalf("count=%d output=%v cache=%q err=%v", count, output.Bytes(), stream.Content, err)
	}
	if _, err := stream.WriteDecodedTo(t.Context(), io.Discard, 5); !errors.Is(err, filter.ErrDecodeLimitExceeded) || errors.Is(err, ErrEncodedStreamSource) {
		t.Fatalf("limit classification: %v", err)
	}
}

func TestWriteDecodedToDomainAndSinkFailuresAreNotSourceFailures(t *testing.T) {
	samples := []byte("samples")
	encoded := streamContextFlate(t, samples)
	damaged := bytes.Clone(encoded)
	damaged[len(damaged)-1] ^= 1
	sinkError := io.EOF
	for _, test := range []struct {
		name    string
		data    []byte
		filters []PDFFilter
		writer  io.Writer
		cause   error
		limit   int64
	}{
		{"checksum", damaged, []PDFFilter{{Name: filter.Flate}}, io.Discard, zlib.ErrChecksum, 1024},
		{"limit", encoded, []PDFFilter{{Name: filter.Flate}}, io.Discard, filter.ErrDecodeLimitExceeded, 2},
		{"sink", samples, nil, streamSourceTestWriter{err: sinkError}, sinkError, 1024},
		{"opaque-nonterminal", samples, []PDFFilter{{Name: filter.JPX}, {Name: filter.Flate}}, io.Discard, filter.ErrUnsupportedFilter, 1024},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &decodeContextSource{data: test.data}
			stream := StreamDict{Dict: NewDict(), FilterPipeline: test.filters}
			if err := stream.SetRawSource(source); err != nil {
				t.Fatal(err)
			}
			_, err := stream.WriteDecodedTo(t.Context(), test.writer, test.limit)
			if !errors.Is(err, test.cause) || errors.Is(err, ErrEncodedStreamSource) || source.closed != 1 {
				t.Fatalf("classification=%v closed=%d", err, source.closed)
			}
		})
	}
}

func TestWriteDecodedToPreservesOpaqueImageStreams(t *testing.T) {
	encoded := []byte("opaque samples")
	for _, name := range []string{filter.DCT, filter.JPX, filter.JBIG2} {
		for _, wrapped := range []bool{false, true} {
			stream := StreamDict{Dict: NewDict(), Raw: encoded, FilterPipeline: []PDFFilter{{Name: name}}}
			if wrapped {
				stream.Raw = streamContextASCII85(t, encoded)
				stream.FilterPipeline = append([]PDFFilter{{Name: filter.ASCII85}}, stream.FilterPipeline...)
			}
			var output bytes.Buffer
			count, err := stream.WriteDecodedTo(t.Context(), &output, 1024)
			if err != nil || count != int64(len(encoded)) || !bytes.Equal(output.Bytes(), encoded) {
				t.Fatalf("%s/wrapped=%t count=%d output=%q err=%v", name, wrapped, count, output.Bytes(), err)
			}
		}
	}
}

func TestWriteDecodedToMaximumLimitDoesNotOverflow(t *testing.T) {
	samples := []byte("maximum limit samples")
	encoded := streamContextFlate(t, samples)
	for _, wrapped := range []bool{false, true} {
		stream := StreamDict{Dict: NewDict(), Raw: encoded, FilterPipeline: []PDFFilter{{Name: filter.Flate}}}
		if wrapped {
			stream.Raw = streamContextASCII85(t, encoded)
			stream.FilterPipeline = append([]PDFFilter{{Name: filter.ASCII85}}, stream.FilterPipeline...)
		}
		var output bytes.Buffer
		count, err := stream.WriteDecodedTo(t.Context(), &output, math.MaxInt64)
		if err != nil || count != int64(len(samples)) || !bytes.Equal(output.Bytes(), samples) {
			t.Fatalf("wrapped=%t count=%d output=%q err=%v", wrapped, count, output.Bytes(), err)
		}
	}
}

func TestWriteDecodedToRetainsSourceErrorThroughTolerantFlate(t *testing.T) {
	encoded := streamContextFlate(t, bytes.Repeat([]byte("source samples"), 1024))
	source := &decodeContextSource{data: encoded, readErr: io.ErrUnexpectedEOF, failAfter: 1, readSize: 6}
	stream := StreamDict{Dict: NewDict(), FilterPipeline: []PDFFilter{{Name: filter.Flate}}}
	if err := stream.SetRawSource(source); err != nil {
		t.Fatal(err)
	}
	_, err := stream.WriteDecodedTo(t.Context(), io.Discard, 1<<20)
	if !errors.Is(err, io.ErrUnexpectedEOF) || !errors.Is(err, ErrEncodedStreamSource) || source.closed != 1 {
		t.Fatalf("source failure lost: %v closed=%d", err, source.closed)
	}
}
