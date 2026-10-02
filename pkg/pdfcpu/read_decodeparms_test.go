/*
Copyright 2018 The pdfcpu Authors.

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
	"bytes"
	"compress/zlib"
	"context"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/filter"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func decodeParmsFlateFixture(t *testing.T, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := zlib.NewWriter(&out)
	if _, err := zw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestFilterDecodeParmsIndirectSampleParity(t *testing.T) {
	ref := func(n int) types.IndirectRef { return *types.NewIndirectRef(n, 0) }
	direct := types.Dict{"Predictor": types.Integer(15), "Colors": types.Integer(3), "BitsPerComponent": types.Integer(8), "Columns": types.Integer(23), "Extension": ref(999)}
	indirect := types.Dict{"Predictor": ref(1), "Colors": ref(2), "BitsPerComponent": ref(3), "Columns": ref(4), "Extension": ref(999)}
	original := indirect.Clone()
	samples := make([]byte, 23*15*3)
	for i := range samples {
		samples[i] = byte(i*37 + 255)
	}
	// With the incorrect default Columns=1, byte four becomes a row filter 255.
	for i := range 6 {
		samples[i] = 255
	}
	var predicted []byte
	for row := range 15 {
		predicted = append(predicted, 0)
		predicted = append(predicted, samples[row*69:(row+1)*69]...)
	}
	raw := decodeParmsFlateFixture(t, predicted)
	for _, tc := range []struct {
		name     string
		filters  types.Object
		parms    types.Object
		outerHex bool
	}{
		{"direct", types.Name(filter.Flate), direct, false},
		{"indirect scalars", types.Name(filter.Flate), indirect, false},
		{"indirect dictionary", types.Name(filter.Flate), ref(5), false},
		{"single array indirect dictionary", types.Name(filter.Flate), types.Array{ref(5)}, false},
		{"single indirect array", types.Name(filter.Flate), ref(6), false},
		{"filter array", types.Array{types.Name(filter.Flate)}, types.Array{ref(5)}, false},
		{"filter array indirect parameters", types.Array{types.Name(filter.Flate)}, ref(6), false},
		{"filter chain", types.Array{types.Name(filter.ASCIIHex), types.Name(filter.Flate)}, ref(7), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, err := model.NewContext(bytes.NewReader(nil), nil)
			if err != nil {
				t.Fatal(err)
			}
			for n, value := range []int{15, 3, 8} {
				ctx.Table[n+1] = model.NewXRefTableEntryGen0(types.Integer(value))
			}
			osd := types.NewObjectStreamDict()
			osd.Raw = decodeParmsFlateFixture(t, []byte("23"))
			osd.MaxDecodeBytes = filter.DefaultMaxDecodeBytes
			ctx.Table[4] = model.NewXRefTableEntryGen0(types.NewLazyObjectStreamObject(osd, 0, -1, compressedObject))
			ctx.Table[5] = model.NewXRefTableEntryGen0(indirect)
			ctx.Table[6] = model.NewXRefTableEntryGen0(types.Array{ref(5)})
			ctx.Table[7] = model.NewXRefTableEntryGen0(types.Array{nil, ref(5)})
			pipeline, err := pdfFilterPipeline(t.Context(), ctx, types.Dict{"Filter": tc.filters, "DecodeParms": tc.parms})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(pipeline[len(pipeline)-1].DecodeParms, direct) {
				t.Fatalf("resolved parameters = %v, want %v", pipeline[len(pipeline)-1].DecodeParms, direct)
			}
			input := raw
			if tc.outerHex {
				input = []byte(hex.EncodeToString(raw) + ">")
			}
			sd := types.StreamDict{Raw: input, FilterPipeline: pipeline}
			if err := sd.Decode(); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(sd.Content, samples) {
				t.Fatal("resolved prediction changed samples")
			}
			pipeline[len(pipeline)-1].DecodeParms["Columns"] = types.Integer(1)
			if !reflect.DeepEqual(indirect, original) || direct["Columns"] != types.Integer(23) {
				t.Fatal("filter parameter normalization mutated the PDF dictionary")
			}
		})
	}
}

func TestFilterDecodeParmsReferenceErrors(t *testing.T) {
	ref := func(n int) types.IndirectRef { return *types.NewIndirectRef(n, 0) }
	for _, tc := range []struct {
		name  string
		value types.Object
		entry types.Object
		want  error
	}{
		{"missing", ref(8), nil, errUnregisteredObject},
		{"cycle", ref(8), ref(8), errCorruptDecodeParms},
		{"wrong indirect type", ref(8), types.Name("NotInteger"), errCorruptDecodeParms},
		{"wrong direct type", types.Boolean(true), nil, errCorruptDecodeParms},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, err := model.NewContext(bytes.NewReader(nil), nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.entry != nil {
				ctx.Table[8] = model.NewXRefTableEntryGen0(tc.entry)
			}
			_, err = pdfFilterPipeline(t.Context(), ctx, types.Dict{"Filter": types.Name(filter.Flate), "DecodeParms": types.Dict{"Columns": tc.value}})
			if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), "Columns") {
				t.Fatalf("error = %v, want %v with parameter context", err, tc.want)
			}
		})
	}
	ctx, err := model.NewContext(bytes.NewReader(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := resolvedFilterDecodeParms(c, ctx, filter.Flate, types.Dict{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled parameter resolution: %v", err)
	}
}

func TestFilterDecodeParmsDefaultsAndBooleans(t *testing.T) {
	ctx, err := model.NewContext(bytes.NewReader(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx.Table[1] = model.NewXRefTableEntryGen0(types.Boolean(true))
	parms, err := resolvedFilterDecodeParms(t.Context(), ctx, filter.CCITTFax, types.Dict{"BlackIs1": *types.NewIndirectRef(1, 0)})
	if err != nil || parms["BlackIs1"] != types.Boolean(true) {
		t.Fatalf("boolean parameters: %v, error %v", parms, err)
	}
	ctx.Table[2] = model.NewXRefTableEntryGen0(types.Integer(0))
	parms, err = resolvedFilterDecodeParms(t.Context(), ctx, filter.LZW, types.Dict{"EarlyChange": *types.NewIndirectRef(2, 0)})
	if err != nil || parms["EarlyChange"] != types.Integer(0) {
		t.Fatalf("LZW parameters: %v, error %v", parms, err)
	}
	payload := []byte{255, 1, 2, 3}
	for _, obj := range []types.Object{nil, types.Dict{}, types.Dict{"Columns": nil}, types.Dict{"Predictor": nil}} {
		parms, err := resolvedFilterDecodeParms(t.Context(), ctx, filter.Flate, obj)
		if err != nil || parms.IntEntry("Columns") != nil {
			t.Fatalf("missing Columns must remain absent: %v, error %v", parms, err)
		}
		sd := types.StreamDict{Raw: decodeParmsFlateFixture(t, payload), FilterPipeline: []types.PDFFilter{{Name: filter.Flate, DecodeParms: parms}}}
		if err := sd.Decode(); err != nil || !bytes.Equal(sd.Content, payload) {
			t.Fatalf("default predictor changed samples: %v", err)
		}
	}
}

func TestFilterDecodeParmsDoesNotSuppressInvalidRowFilter(t *testing.T) {
	sd := types.StreamDict{
		Raw:            decodeParmsFlateFixture(t, []byte{255, 1, 2, 3}),
		FilterPipeline: []types.PDFFilter{{Name: filter.Flate, DecodeParms: types.Dict{"Predictor": types.Integer(15), "Columns": types.Integer(1), "Colors": types.Integer(3)}}},
	}
	if err := sd.Decode(); err == nil || !strings.Contains(err.Error(), "unexpected PNG predictor 255") {
		t.Fatalf("invalid row filter was suppressed: %v", err)
	}
}
