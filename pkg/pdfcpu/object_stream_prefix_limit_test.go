package pdfcpu

import (
	"encoding/hex"
	"errors"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/filter"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestParseObjectStreamRejectsFirstBeyondDecodeLimit(t *testing.T) {
	const content = "1 0 42"
	osd := &types.ObjectStreamDict{
		StreamDict: types.StreamDict{
			Raw:            []byte(hex.EncodeToString([]byte(content)) + ">"),
			FilterPipeline: []types.PDFFilter{{Name: filter.ASCIIHex}},
		},
		FirstObjOffset: 4,
		ObjCount:       1,
	}
	limits := model.DefaultResourceLimits()
	limits.MaxDecodeBytes = 3
	if err := parseObjectStream(t.Context(), osd, limits); !errors.Is(err, filter.ErrDecodeLimitExceeded) {
		t.Fatalf("got %v, want %v", err, filter.ErrDecodeLimitExceeded)
	}
}
