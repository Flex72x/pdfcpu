package model

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/filter"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func pageContentLimitFixture(stream types.StreamDict, placement string) (*XRefTable, types.Dict) {
	table := &XRefTable{Conf: &Configuration{}, Table: map[int]*XRefTableEntry{7: NewXRefTableEntryGen0(stream)}}
	var contents types.Object = stream
	switch placement {
	case "indirect":
		contents = *types.NewIndirectRef(7, 0)
	case "array":
		contents = types.Array{*types.NewIndirectRef(7, 0)}
	case "indirect-array":
		table.Table[8] = NewXRefTableEntryGen0(types.Array{*types.NewIndirectRef(7, 0)})
		contents = *types.NewIndirectRef(8, 0)
	}
	return table, types.Dict{"Contents": contents}
}

func TestPageContentConfiguredDecodeLimit(t *testing.T) {
	samples := bytes.Repeat([]byte("q Q\n"), 16)
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(samples); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []int{ValidationStrict, ValidationRelaxed} {
		for _, placement := range []string{"direct", "indirect", "array", "indirect-array"} {
			for _, encoding := range []string{"raw", "flate", "decoded"} {
				t.Run(fmt.Sprintf("%d/%s/%s", mode, placement, encoding), func(t *testing.T) {
					stream := types.StreamDict{Raw: samples}
					if encoding == "flate" {
						stream.Raw = compressed.Bytes()
						stream.FilterPipeline = []types.PDFFilter{{Name: filter.Flate}}
					}
					if encoding == "decoded" {
						stream.Content = samples
					}
					table, page := pageContentLimitFixture(stream, placement)
					table.ValidationMode = mode
					table.Conf.Limits.MaxDecodeBytes = int64(len(samples) / 2)
					if _, err := table.PageContent(page, 1); !errors.Is(err, filter.ErrDecodeLimitExceeded) {
						t.Fatalf("expected decode limit, got %v", err)
					}
					if !reflect.DeepEqual(table.Table[7].Object, stream) {
						t.Fatal("rejected stream changed")
					}
					table.Conf.Limits.MaxDecodeBytes = int64(len(samples))
					actual, err := table.PageContent(page, 1)
					if err != nil || !bytes.Equal(actual, samples) {
						t.Fatalf("exact limit rejected: content=%q, err=%v", actual, err)
					}
				})
			}
		}
	}
}

func TestPageContentZeroDecodeLimitKeepsDefault(t *testing.T) {
	stream := types.StreamDict{Raw: []byte("q Q")}
	table, page := pageContentLimitFixture(stream, "indirect")
	actual, err := table.PageContent(page, 1)
	if err != nil || !bytes.Equal(actual, stream.Raw) {
		t.Fatalf("default limit rejected: content=%q, err=%v", actual, err)
	}
}

func TestPageContentDecodeLimitPreservesValidationPolicy(t *testing.T) {
	stream := types.StreamDict{
		Raw:            corruptZlibContent(t, []byte("q Q")),
		FilterPipeline: []types.PDFFilter{{Name: filter.Flate}},
	}
	for _, mode := range []int{ValidationStrict, ValidationRelaxed} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			table, page := pageContentLimitFixture(stream, "array")
			table.ValidationMode = mode
			table.Conf.Limits.MaxDecodeBytes = 16
			for range 2 {
				_, err := table.PageContent(page, 1)
				if mode == ValidationStrict && !errors.Is(err, zlib.ErrChecksum) {
					t.Fatalf("strict checksum handling changed: %v", err)
				}
				if mode == ValidationRelaxed && !errors.Is(err, ErrNoContent) {
					t.Fatalf("relaxed checksum handling changed: %v", err)
				}
				if !reflect.DeepEqual(table.Table[7].Object, stream) {
					t.Fatal("failed decode cached or source changed")
				}
			}
		})
	}
}
