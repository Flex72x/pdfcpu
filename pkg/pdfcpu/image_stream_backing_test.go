package pdfcpu

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestSpillImageStreamsReleasesXrefAndOptimizationBuffers(t *testing.T) {
	doc := sourceTestContext(t)
	doc.Optimize = &model.OptimizationContext{ImageObjects: map[int]*model.ImageObject{}, DuplicateImages: map[int]*model.DuplicateImageObject{}}
	samples := bytes.Repeat([]byte{12, 34, 56}, 8*8)
	sd := sourceTestStream(sourceTestFlate(t, samples), types.Name(model.DeviceRGBCS), 8, 8, 8)
	sd.Dict["Type"], sd.Dict["Subtype"] = types.Name("XObject"), types.Name("Image")
	sd.Dict["Filter"] = types.Name("FlateDecode")
	sd.FilterPipeline = []types.PDFFilter{{Name: "FlateDecode"}}
	length := int64(len(sd.Raw))
	sd.StreamLength = &length
	sd.Dict["Length"] = types.Integer(length)
	ref, err := doc.IndRefForNewObject(*sd)
	if err != nil {
		t.Fatal(err)
	}
	number := ref.ObjectNumber.Value()
	doc.Optimize.ImageObjects[number] = &model.ImageObject{ImageDict: sd}
	duplicate := sd.Clone().(types.StreamDict)
	doc.Optimize.DuplicateImages[number] = &model.DuplicateImageObject{ImageDict: &duplicate}
	spilled, err := SpillImageStreams(t.Context(), doc, t.TempDir())
	if err != nil || spilled != length {
		t.Fatalf("spill: %d %v", spilled, err)
	}
	entry, _ := doc.FindTableEntry(number, 0)
	backed := entry.Object.(types.StreamDict)
	for _, stream := range []*types.StreamDict{&backed, sd, &duplicate} {
		if stream.Raw != nil || stream.Content != nil || stream.RawSource == nil || stream.RawLength() != length {
			t.Fatal("xref/cache retained an image buffer")
		}
	}
	source, err := ExtractImageSource(t.Context(), doc, &backed)
	if err != nil || !bytes.Equal(source.Data, samples) {
		t.Fatalf("source samples changed after spill: %v", err)
	}
	if backed.Content != nil {
		t.Fatal("source extraction retained a decoded cache")
	}
	var output bytes.Buffer
	if n, err := backed.WriteRawTo(&output); err != nil || n != length || !bytes.Equal(output.Bytes(), sourceTestFlate(t, samples)) {
		t.Fatalf("writer changed encoded samples: %d %v", n, err)
	}
}

func TestSpillImageStreamsCancellationAndDestinationFailure(t *testing.T) {
	doc := sourceTestContext(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := SpillImageStreams(ctx, doc, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	sd := types.StreamDict{Dict: types.Dict{"Type": types.Name("XObject"), "Subtype": types.Name("Image")}, Raw: []byte("original")}
	ref, err := doc.IndRefForNewObject(sd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SpillImageStreams(t.Context(), doc, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing destination accepted")
	}
	entry, _ := doc.FindTableEntry(ref.ObjectNumber.Value(), 0)
	if !bytes.Equal(entry.Object.(types.StreamDict).Raw, sd.Raw) {
		t.Fatal("failed sink changed original stream")
	}
}
