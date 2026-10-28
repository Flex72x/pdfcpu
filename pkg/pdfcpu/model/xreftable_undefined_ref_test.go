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
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// TestInsertNewSkipsUndefinedReferences verifies that a new object never takes the number of an
// object that is referenced but undefined, since the reference would then resolve to the new object.
func TestInsertNewSkipsUndefinedReferences(t *testing.T) {
	size := 3
	xRefTable := &XRefTable{Table: map[int]*XRefTableEntry{0: {Free: true}, 1: {}, 2: {}}, Size: &size}

	xRefTable.IncrementRefCount(types.NewIndirectRef(1, 0))
	xRefTable.IncrementRefCount(types.NewIndirectRef(3, 0))
	xRefTable.IncrementRefCount(types.NewIndirectRef(4, 0))
	xRefTable.IncrementRefCount(nil)

	if got := xRefTable.Table[1].RefCount; got != 1 {
		t.Fatalf("RefCount of defined object = %d, want 1", got)
	}
	if got := xRefTable.InsertNew(XRefTableEntry{}); got != 5 {
		t.Fatalf("InsertNew = %d, want 5", got)
	}
	if size != 6 {
		t.Fatalf("Size = %d, want 6", size)
	}
	if got := xRefTable.InsertNew(XRefTableEntry{}); got != 6 {
		t.Fatalf("second InsertNew = %d, want 6", got)
	}
	for _, objNr := range []int{3, 4} {
		if _, found := xRefTable.Table[objNr]; found {
			t.Fatalf("undefined referenced object %d was allocated", objNr)
		}
	}
}

// TestRecycledObjectSkipsReferencedFreeNumbers verifies that a free entry still referenced by an object
// (a dangling reference, e.g. to a cross-reference stream number) is not recycled for a new object.
func TestRecycledObjectSkipsReferencedFreeNumbers(t *testing.T) {
	size := 5
	offset := func(v int64) *int64 { return &v }
	gen := func(v int) *int { return &v }
	xRefTable := &XRefTable{Table: map[int]*XRefTableEntry{
		0: {Free: true, Offset: offset(2), Generation: gen(types.FreeHeadGeneration)},
		1: {},
		2: {Free: true, Offset: offset(3), Generation: gen(0)},
		3: {Free: true, Offset: offset(0), Generation: gen(0)},
		4: {},
	}, Size: &size}

	xRefTable.IncrementRefCount(types.NewIndirectRef(2, 0))
	if got := xRefTable.Table[2].RefCount; got != 0 {
		t.Fatalf("RefCount of free object = %d, want 0", got)
	}

	ref, err := xRefTable.IndRefForNewObject(types.Array{})
	if err != nil {
		t.Fatal(err)
	}
	if got := ref.ObjectNumber.Value(); got != 3 {
		t.Fatalf("first new object = %d, want 3", got)
	}
	if !xRefTable.Table[2].Free || *xRefTable.Table[2].Offset != 0 {
		t.Fatalf("referenced free entry changed: %+v", xRefTable.Table[2])
	}
	if ref, err = xRefTable.IndRefForNewObject(types.Array{}); err != nil {
		t.Fatal(err)
	}
	if got := ref.ObjectNumber.Value(); got != 5 {
		t.Fatalf("second new object = %d, want 5", got)
	}
}
