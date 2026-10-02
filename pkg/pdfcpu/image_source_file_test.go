package pdfcpu

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/filter"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestWriteRasterSourceMatchesImmutableExtraction(t *testing.T) {
	doc := sourceTestContext(t)
	for _, bpc := range []int{1, 2, 4, 8, 16} {
		for _, cs := range []string{model.DeviceGrayCS, model.DeviceRGBCS} {
			components := sourceSpaceComponents(cs)
			width, height := 19, 11
			stride := (width*components*bpc + 7) / 8
			samples := make([]byte, stride*height)
			for i := range samples {
				samples[i] = byte(i * 17)
			}
			sd := sourceTestStream(sourceTestFlate(t, samples), types.Name(cs), bpc, width, height)
			sd.FilterPipeline = []types.PDFFilter{{Name: filter.Flate}}
			path := filepath.Join(t.TempDir(), "encoded.flate")
			if err := os.WriteFile(path, sd.Raw, 0600); err != nil {
				t.Fatal(err)
			}
			source, err := types.NewFileStreamSource(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = sd.SetRawSource(source); err != nil {
				t.Fatal(err)
			}
			before := sd.Clone().(types.StreamDict)
			expected, err := ExtractImageSource(t.Context(), doc, sd)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			actual, err := WriteRasterImageSource(t.Context(), doc, sd, &output)
			if err != nil {
				t.Fatal(err)
			}
			if actual.Data != nil {
				t.Fatal("file source retained decoded samples")
			}
			if !bytes.Equal(expected.Data, output.Bytes()) {
				t.Fatal("samples changed")
			}
			expected.Data = nil
			if !reflect.DeepEqual(expected, actual) {
				t.Fatal("source facts changed")
			}
			if !reflect.DeepEqual(*sd, before) {
				t.Fatal("source cache/dictionary mutated")
			}
		}
	}
}
func TestWriteRasterLargeRawSourceDoesNotAllocateSamples(t *testing.T) {
	doc := sourceTestContext(t)
	width, height := 12000, 7000
	path := filepath.Join(t.TempDir(), "samples.raw")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(int64(width * height * 3)); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	backing, err := types.NewFileStreamSource(path)
	if err != nil {
		t.Fatal(err)
	}
	sd := sourceTestStream(nil, types.Name(model.DeviceRGBCS), 8, width, height)
	if err = sd.SetRawSource(backing); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	facts, err := WriteRasterImageSource(t.Context(), doc, sd, io.Discard)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if facts.Data != nil || after.TotalAlloc-before.TotalAlloc > 4<<20 {
		t.Fatalf("large samples allocated: %d", after.TotalAlloc-before.TotalAlloc)
	}
	doc.Limits.MaxDecodeBytes = 1
	sd.FilterPipeline = []types.PDFFilter{{Name: filter.Flate}}
	sd.RawSource = nil
	sd.Raw = sourceTestFlate(t, []byte{1, 2, 3})
	if _, err = WriteRasterImageSource(t.Context(), doc, sd, io.Discard); !errors.Is(err, ErrImageSourceLimit) {
		t.Fatalf("decode limit: %v", err)
	}
}
