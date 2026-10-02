package model

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestPNGFileStreamMatchesDecodedModel(t *testing.T) {
	b := image.Rect(0, 0, 19, 13)
	gray := image.NewGray(b)
	gray16 := image.NewGray16(b)
	rgb := image.NewNRGBA(b)
	rgba := image.NewNRGBA(b)
	rgb16 := image.NewNRGBA64(b)
	rgba16 := image.NewNRGBA64(b)
	grayAlpha := image.NewNRGBA(b)
	grayAlpha16 := image.NewNRGBA64(b)
	pal := image.NewPaletted(b, color.Palette{color.NRGBA{255, 0, 0, 255}, color.NRGBA{0, 255, 0, 0}, color.NRGBA{0, 0, 255, 77}})
	palOpaque := image.NewPaletted(b, color.Palette{color.Black, color.White})
	for y := range b.Dy() {
		for x := range b.Dx() {
			r, g, v := uint8(x*11), uint8(y*17), uint8((x+y)*7)
			gray.SetGray(x, y, color.Gray{v})
			gray16.SetGray16(x, y, color.Gray16{uint16(v)*257 + uint16(x)})
			rgb.SetNRGBA(x, y, color.NRGBA{r, g, v, 255})
			rgba.SetNRGBA(x, y, color.NRGBA{r, g, v, uint8(x * y)})
			rgb16.SetNRGBA64(x, y, color.NRGBA64{uint16(r)*257 + 1, uint16(g)*257 + 5, uint16(v)*257 + 9, 65535})
			rgba16.SetNRGBA64(x, y, color.NRGBA64{uint16(r)*257 + 1, uint16(g)*257 + 5, uint16(v)*257 + 9, uint16(x*y) * 131})
			grayAlpha.SetNRGBA(x, y, color.NRGBA{v, v, v, uint8(x * y)})
			grayAlpha16.SetNRGBA64(x, y, color.NRGBA64{uint16(v) * 257, uint16(v) * 257, uint16(v) * 257, uint16(x*y) * 131})
			pal.SetColorIndex(x, y, uint8((x+y)%3))
			palOpaque.SetColorIndex(x, y, uint8((x+y)%2))
		}
	}
	for name, im := range map[string]image.Image{"gray": gray, "gray16": gray16, "rgb": rgb, "rgba": rgba, "rgb16": rgb16, "rgba16": rgba16, "grayAlpha": grayAlpha, "grayAlpha16": grayAlpha16, "palette": pal, "paletteOpaque": palOpaque} {
		t.Run(name, func(t *testing.T) {
			var data bytes.Buffer
			if err := png.Encode(&data, im); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "source.png")
			if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			conf := NewDefaultConfiguration()
			conf.ValidationMode = ValidationRelaxed
			oldXref := pngFileTestXref(conf)
			fileXref := pngFileTestXref(conf)
			expected, _, _, err := CreateImageStreamDict(oldXref, bytes.NewReader(data.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			actual, err := CreatePNGImageStreamDictFromFile(t.Context(), fileXref, path, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			comparePNGFileStream(t, expected, actual)
			if actual.Raw != nil || actual.Content != nil || actual.RawSource == nil {
				t.Fatal("encoded samples retained in memory")
			}
			a, e := actual.Dict["SMask"], expected.Dict["SMask"]
			if (a == nil) != (e == nil) {
				t.Fatalf("mask presence differs: %v %v", a, e)
			}
			if a != nil {
				as, _, err := fileXref.DereferenceStreamDict(a)
				if err != nil {
					t.Fatal(err)
				}
				es, _, err := oldXref.DereferenceStreamDict(e)
				if err != nil {
					t.Fatal(err)
				}
				comparePNGFileStream(t, es, as)
			}
		})
	}
}
func comparePNGFileStream(t *testing.T, expected, actual *types.StreamDict) {
	t.Helper()
	for _, key := range []string{"ColorSpace", "Width", "Height", "BitsPerComponent", "Interpolate", "Filter"} {
		a, e := actual.Dict[key], expected.Dict[key]
		if a == nil && e == nil {
			continue
		}
		if a == nil || e == nil || a.String() != e.String() {
			t.Fatalf("%s differs: %v %v", key, a, e)
		}
	}
	actualRaw, err := actual.RawBytes(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actualRaw, expected.Raw) {
		t.Fatal("encoded Flate stream bytes differ")
	}
	if err := expected.Decode(); err != nil {
		t.Fatal(err)
	}
	if err := actual.Decode(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual.Content, expected.Content) {
		t.Fatalf("decoded samples differ (%d vs %d)", len(actual.Content), len(expected.Content))
	}
	// Decoded caches are caller-owned; the file constructor itself keeps none.
	actual.Content = nil
}

func TestPNGFileStreamRejectsCancellationAndDamage(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewGray(image.Rect(0, 0, 10, 10))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source.png")
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := CreatePNGImageStreamDictFromFile(c, pngFileTestXref(NewDefaultConfiguration()), path, t.TempDir()); err != context.Canceled {
		t.Fatal(err)
	}
	damaged := bytes.Clone(data.Bytes())
	damaged[len(damaged)-1] ^= 1
	if err := os.WriteFile(path, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CreatePNGImageStreamDictFromFile(t.Context(), pngFileTestXref(NewDefaultConfiguration()), path, t.TempDir()); err == nil {
		t.Fatal("damaged checksum accepted")
	}
}

func pngFileTestXref(conf *Configuration) *XRefTable {
	xref := newXRefTable(conf)
	size, zero, gen := 1, int64(0), 65535
	xref.Size = &size
	xref.Table[0] = &XRefTableEntry{Free: true, Offset: &zero, Generation: &gen}
	return xref
}

func TestPNGFileStreamLargeUsesBoundedRetainedHeap(t *testing.T) {
	path := os.Getenv("PDFCPU_LARGE_PNG_SOURCE")
	if path == "" {
		t.Skip("large PNG heap measurement is opt-in")
	}
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	stop := make(chan struct{})
	peak := make(chan uint64, 1)
	go func() {
		var maximum uint64
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				peak <- maximum
				return
			case <-ticker.C:
				var stats runtime.MemStats
				runtime.ReadMemStats(&stats)
				if stats.HeapAlloc > maximum {
					maximum = stats.HeapAlloc
				}
			}
		}
	}()
	sd, err := CreatePNGImageStreamDictFromFile(t.Context(), pngFileTestXref(NewDefaultConfiguration()), path, t.TempDir())
	close(stop)
	maximum := <-peak
	if err != nil {
		t.Fatal(err)
	}
	if sd.Raw != nil || sd.Content != nil || sd.RawSource == nil {
		t.Fatal("large image retained encoded/decoded buffers")
	}
	delta := int64(maximum) - int64(before.HeapAlloc)
	t.Logf("geometry=%dx%d sampled_peak_heap_delta=%d encoded_bytes=%d", *sd.IntEntry("Width"), *sd.IntEntry("Height"), delta, sd.RawLength())
	if delta > 64<<20 {
		t.Fatalf("full raster retained in Go heap: %d", delta)
	}
}
