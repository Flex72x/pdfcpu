/*
Copyright 2026 The pdfcpu Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package pdfcpu

import (
	"math"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// TestAddWatermarksKeepsSharedPageContent covers a document whose pages share one content stream and one of
// them is rotated: the stamp of each page must not leak into the others through the shared stream.
func TestAddWatermarksKeepsSharedPageContent(t *testing.T) {
	ctx := testOptimizeContext(t)
	pagesIndRef, err := ctx.Pages()
	if err != nil {
		t.Fatal(err)
	}
	pagesDict, err := ctx.DereferenceDict(*pagesIndRef)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := ctx.NewStreamDictForBuf([]byte("q"))
	if err != nil {
		t.Fatal(err)
	}
	if err := shared.Encode(); err != nil {
		t.Fatal(err)
	}
	sharedRef, err := ctx.IndRefForNewObject(*shared)
	if err != nil {
		t.Fatal(err)
	}
	ownStream, err := ctx.NewStreamDictForBuf([]byte("0 0 10 10 re f Q"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ownStream.Encode(); err != nil {
		t.Fatal(err)
	}
	ownRef, err := ctx.IndRefForNewObject(*ownStream)
	if err != nil {
		t.Fatal(err)
	}

	mediaBox := types.NewRectangle(10, 20, 410, 620)
	for i, rotate := range []int{0, 270} {
		pageIndRef, err := ctx.EmptyPage(pagesIndRef, mediaBox, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := model.AppendPageTree(pageIndRef, 1, pagesDict); err != nil {
			t.Fatal(err)
		}
		ctx.PageCount = i + 1
		d, err := ctx.DereferenceDict(*pageIndRef)
		if err != nil {
			t.Fatal(err)
		}
		d["Contents"] = types.Array{*sharedRef, *ownRef}
		if rotate != 0 {
			d["Rotate"] = types.Integer(rotate)
		}
	}

	wm, err := ParseTextWatermarkDetails(t.Context(), "7", "pos:bc, scale:1 abs, rot:0", true, types.POINTS, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := AddWatermarks(t.Context(), ctx, nil, wm); err != nil {
		t.Fatal(err)
	}

	for _, ref := range []types.IndirectRef{*sharedRef, *ownRef} {
		sd, _, err := ctx.DereferenceStreamDict(ref)
		if err != nil {
			t.Fatal(err)
		}
		if err := sd.Decode(); err != nil {
			t.Fatal(err)
		}
		want := map[int]string{sharedRef.ObjectNumber.Value(): "q", ownRef.ObjectNumber.Value(): "0 0 10 10 re f Q"}[ref.ObjectNumber.Value()]
		if string(sd.Content) != want {
			t.Fatalf("page content stream obj#%d changed to %q", ref.ObjectNumber.Value(), sd.Content)
		}
	}

	for pageNr, rotate := range map[int]int{1: 0, 2: 270} {
		d, _, attrs, err := ctx.PageDict(t.Context(), pageNr, false)
		if err != nil {
			t.Fatal(err)
		}
		if attrs.Rotate != rotate || !attrs.MediaBox.Equals(*mediaBox) {
			t.Fatalf("page %d geometry changed: rotate %d, media box %v", pageNr, attrs.Rotate, attrs.MediaBox)
		}
		contents, ok := d["Contents"].(types.Array)
		if !ok || len(contents) != 4 || contents[1] != *sharedRef || contents[2] != *ownRef {
			t.Fatalf("page %d contents = %v, want stamp streams around the page streams", pageNr, d["Contents"])
		}
	}
}

// TestNormalizePageWatermarkMapsDisplayedPage checks that the displayed corners of a page, for every rotation
// and an offset visible region, land on the matching corners of the page in user space.
func TestNormalizePageWatermarkMapsDisplayedPage(t *testing.T) {
	box := types.NewRectangle(10, 20, 410, 620) // 400 x 600
	tests := []struct {
		rotate                 int
		width, height          float64
		lowerLeft, bottomRight types.Point // user space points shown at the displayed lower left and lower right
	}{
		{rotate: 0, width: 400, height: 600, lowerLeft: types.Point{X: 10, Y: 20}, bottomRight: types.Point{X: 410, Y: 20}},
		{rotate: 90, width: 600, height: 400, lowerLeft: types.Point{X: 410, Y: 20}, bottomRight: types.Point{X: 410, Y: 620}},
		{rotate: 180, width: 400, height: 600, lowerLeft: types.Point{X: 410, Y: 620}, bottomRight: types.Point{X: 10, Y: 620}},
		{rotate: -90, width: 600, height: 400, lowerLeft: types.Point{X: 10, Y: 620}, bottomRight: types.Point{X: 10, Y: 20}},
		{rotate: 45, width: 400, height: 600, lowerLeft: types.Point{X: 10, Y: 20}, bottomRight: types.Point{X: 410, Y: 20}},
	}
	for _, tt := range tests {
		wm := model.DefaultWatermarkConfig()
		if err := normalizePageWatermark(&model.InheritedPageAttrs{MediaBox: box, Rotate: tt.rotate}, wm); err != nil {
			t.Fatal(err)
		}
		if wm.Vp.LL.X != 0 || wm.Vp.LL.Y != 0 || wm.Vp.Width() != tt.width || wm.Vp.Height() != tt.height {
			t.Fatalf("rotate %d: viewport %v, want 0 0 %v %v", tt.rotate, wm.Vp, tt.width, tt.height)
		}
		for _, c := range []struct{ displayed, want types.Point }{
			{types.Point{X: 0, Y: 0}, tt.lowerLeft},
			{types.Point{X: tt.width, Y: 0}, tt.bottomRight},
		} {
			got := wm.PageTransform.Transform(c.displayed)
			if math.Abs(got.X-c.want.X) > 1e-9 || math.Abs(got.Y-c.want.Y) > 1e-9 {
				t.Fatalf("rotate %d: displayed %v maps to %v, want %v", tt.rotate, c.displayed, got, c.want)
			}
		}
	}
}
