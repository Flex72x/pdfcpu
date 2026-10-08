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
