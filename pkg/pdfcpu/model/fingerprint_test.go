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

package model

import (
	"math"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestEqualObjectsNull(t *testing.T) {
	xRefTable := newXRefTable(NewDefaultConfiguration())
	for _, tc := range []struct {
		o1, o2 types.Object
		want   bool
	}{
		{nil, nil, true},
		{nil, types.Integer(0), false},
		{types.Integer(0), nil, false},
		{types.Array{nil}, types.Array{nil}, true},
		{types.Array{nil}, types.Array{types.Name("X")}, false},
	} {
		got, err := EqualObjects(t.Context(), tc.o1, tc.o2, xRefTable, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Fatalf("EqualObjects(%v, %v) = %t, want %t", tc.o1, tc.o2, got, tc.want)
		}
	}
}

// TestFingerprintIsNecessaryForEquality checks the contract duplicate lookup
// relies on: equal objects share a fingerprint, and objects with different
// exact fingerprints are never equal.
func TestFingerprintIsNecessaryForEquality(t *testing.T) {
	xRefTable := newXRefTable(NewDefaultConfiguration())
	xRefTable.Table[3] = NewXRefTableEntryGen0(types.Dict{"N": types.Integer(1)})
	xRefTable.Table[4] = NewXRefTableEntryGen0(types.Dict{"N": types.Integer(1)})
	xRefTable.Table[5] = NewXRefTableEntryGen0(types.Dict{"N": types.Integer(2)})
	ref := func(objNr int) types.IndirectRef { return *types.NewIndirectRef(objNr, 0) }
	stream := func(raw string, parms types.Object) types.StreamDict {
		return types.StreamDict{Dict: types.Dict{"Length": types.Integer(len(raw)), "DecodeParms": parms}, Raw: []byte(raw)}
	}

	objects := []types.Object{
		nil,
		types.Integer(1),
		types.Float(1),
		types.Float(0),
		types.Float(math.Copysign(0, -1)),
		types.Name("A"),
		types.StringLiteral("A"),
		types.HexLiteral("A"),
		types.Boolean(true),
		types.Array{nil},
		types.Array{types.Integer(1), nil},
		ref(3),
		ref(4),
		ref(5),
		ref(99),
		types.Dict{"A": ref(3)},
		types.Dict{"A": ref(4)},
		types.Dict{"A": ref(5)},
		types.Dict{"A": types.Dict{"N": types.Integer(1)}},
		stream("abc", types.Array{nil}),
		stream("abc", types.Array{nil}),
		stream("abc", types.Array{types.Dict{}}),
		stream("abd", types.Array{nil}),
	}
	cache := FingerprintCache{}
	fps := make([]ObjectFingerprint, len(objects))
	for i, o := range objects {
		fp, err := Fingerprint(t.Context(), o, xRefTable, cache)
		if err != nil {
			t.Fatal(err)
		}
		if !fp.Exact {
			t.Fatalf("object %d (%v) has no exact fingerprint", i, o)
		}
		fps[i] = fp
	}
	for i := range objects {
		for j := range objects {
			equal, err := EqualObjects(t.Context(), objects[i], objects[j], xRefTable, nil)
			if err != nil {
				t.Fatal(err)
			}
			if equal != (fps[i].Sum == fps[j].Sum) {
				t.Fatalf("objects %d and %d: equal=%t, same fingerprint=%t", i, j, equal, fps[i].Sum == fps[j].Sum)
			}
		}
	}
}

func TestFingerprintInexactCases(t *testing.T) {
	xRefTable := newXRefTable(NewDefaultConfiguration())
	xRefTable.Table[3] = NewXRefTableEntryGen0(types.Dict{"Self": *types.NewIndirectRef(3, 0)})
	for name, o := range map[string]types.Object{
		"cycle":        *types.NewIndirectRef(3, 0),
		"font":         types.Dict{"Type": types.Name("Font"), "BaseFont": types.Name("ABCDEF+Arial")},
		"nested font":  types.Array{types.Dict{"Type": types.Name("Font")}},
		"empty stream": types.StreamDict{Dict: types.Dict{}},
	} {
		fp, err := Fingerprint(t.Context(), o, xRefTable, FingerprintCache{})
		if err != nil {
			t.Fatal(err)
		}
		if fp.Exact {
			t.Fatalf("%s: fingerprint must not be exact", name)
		}
	}
}
