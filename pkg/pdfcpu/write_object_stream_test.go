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
	"bytes"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// TestWriteContextPacksCatalogObjectsIntoObjectStreams verifies that objects
// outside the page tree (here optional content groups) are written into object
// streams and still resolve after reading the result back.
func TestWriteContextPacksCatalogObjectsIntoObjectStreams(t *testing.T) {
	conf := model.NewDefaultConfiguration()
	ctx := newInfoWriteContext(t, conf, nil)
	root, err := ctx.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	const groups = 250
	ocgs := make(types.Array, 0, groups)
	for range groups {
		ref, err := ctx.IndRefForNewObject(types.Dict{"Type": types.Name("OCG"), "Name": types.StringLiteral("layer")})
		if err != nil {
			t.Fatal(err)
		}
		ocgs = append(ocgs, *ref)
	}
	root["OCProperties"] = types.Dict{"OCGs": ocgs, "D": types.Dict{"Order": ocgs}}

	output := writeInfoContext(t, ctx)
	if bytes.Contains(output, []byte("/OCG")) {
		t.Fatal("optional content groups are written outside object streams")
	}

	read, err := Read(t.Context(), bytes.NewReader(output), conf)
	if err != nil {
		t.Fatal(err)
	}
	root, err = read.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	properties, err := read.DereferenceDict(root["OCProperties"])
	if err != nil {
		t.Fatal(err)
	}
	stored, err := read.DereferenceArray(properties["OCGs"])
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != groups {
		t.Fatalf("read %d optional content groups, want %d", len(stored), groups)
	}
	for _, o := range stored {
		d, err := read.DereferenceDict(o)
		if err != nil {
			t.Fatal(err)
		}
		if name := d.NameEntry("Type"); name == nil || *name != "OCG" {
			t.Fatalf("optional content group reads as %v", d)
		}
	}
}
