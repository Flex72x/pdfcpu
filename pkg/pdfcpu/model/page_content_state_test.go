package model

import (
	"bytes"
	"compress/zlib"
	"errors"
	"io"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/filter"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

type pageContentCloseFailure struct {
	data  []byte
	cause error
}

func (source pageContentCloseFailure) Size() int64 { return int64(len(source.data)) }

func (source pageContentCloseFailure) Open() (io.ReadCloser, error) {
	return pageContentFaultReader{Reader: bytes.NewReader(source.data), cause: source.cause}, nil
}

type pageContentFaultReader struct {
	*bytes.Reader
	cause error
}

func (reader pageContentFaultReader) Close() error { return reader.cause }

func TestPageContentStatePreservesFailedStreams(t *testing.T) {
	stream := types.StreamDict{Raw: corruptZlibContent(t, []byte("/F1 10 Tf")), FilterPipeline: []types.PDFFilter{{Name: filter.Flate}}}
	table, page := pageContentLimitFixture(stream, "array")
	table.ValidationMode = ValidationRelaxed
	resources := types.Dict{"Font": types.Dict{"F1": types.Name("preserved")}}
	called := false
	err := table.processPageContent(t.Context(), page, resources, 1, func(data *PageContentData) error {
		called = true
		if !errors.Is(data.DecodeError, zlib.ErrChecksum) || len(data.EmptyStreams) != 0 {
			t.Fatalf("failed stream classified empty: %+v", data)
		}
		return nil
	}, true)
	if err != nil || !called {
		t.Fatalf("called=%t, err=%v", called, err)
	}
	if resources.DictEntry("Font")["F1"] == nil {
		t.Fatal("failed stream resources pruned")
	}
}

func TestPageContentStateDoesNotSuppressSourceFailure(t *testing.T) {
	cause := errors.New("backing close failed")
	source := pageContentCloseFailure{data: corruptZlibContent(t, []byte("q Q")), cause: cause}
	stream := types.StreamDict{Dict: types.NewDict(), FilterPipeline: []types.PDFFilter{{Name: filter.Flate}}}
	if err := stream.SetRawSource(source); err != nil {
		t.Fatal(err)
	}
	table, page := pageContentLimitFixture(stream, "array")
	table.ValidationMode = ValidationRelaxed
	data := PageContentData{EmptyStreams: types.IntSet{}}
	_, err := table.pageContent(t.Context(), page, 1, &data)
	if !errors.Is(err, cause) || !errors.Is(err, types.ErrEncodedStreamSource) {
		t.Fatalf("source failure suppressed: %v", err)
	}
}
