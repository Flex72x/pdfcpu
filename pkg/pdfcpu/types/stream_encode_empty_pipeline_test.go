package types

import (
	"bytes"
	"testing"
)

func TestEncodeUnfilteredStreamClone(t *testing.T) {
	for _, pipeline := range [][]PDFFilter{nil, {}} {
		stream := StreamDict{Dict: NewDict(), Content: []byte("new content"), FilterPipeline: pipeline}
		cloned := stream.Clone().(StreamDict)
		if err := cloned.Encode(); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(cloned.Raw, stream.Content) || cloned.StreamLength == nil || *cloned.StreamLength != int64(len(stream.Content)) {
			t.Fatalf("invalid unfiltered encoding: %+v", cloned)
		}
	}
}
