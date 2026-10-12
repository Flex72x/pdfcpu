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
	"context"
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"maps"
	"math"
	"slices"

	"github.com/pdfcpu/pdfcpu/internal/contextutil"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// ObjectFingerprint is a necessary condition for EqualObjects: two objects
// with exact fingerprints that differ are never equal. Objects whose equality
// EqualObjects decides by rules a content hash cannot express (reference
// cycles, font name prefixes, unsupported types) are
// not exact and must still be compared against every candidate.
type ObjectFingerprint struct {
	Sum   [sha256.Size]byte
	Exact bool
}

// FingerprintCache memoizes fingerprints of indirect objects by object number,
// so shared subtrees such as soft masks or ICC profiles are hashed once.
type FingerprintCache map[int]ObjectFingerprint

// Fingerprint returns the fingerprint of o in the context of xRefTable.
// The cache may be nil.
func Fingerprint(c context.Context, o types.Object, xRefTable *XRefTable, cache FingerprintCache) (ObjectFingerprint, error) {
	f := fingerprinter{c: c, xRefTable: xRefTable, cache: cache, active: map[int]bool{}}
	return f.object(o)
}

type fingerprinter struct {
	c         context.Context
	xRefTable *XRefTable
	cache     FingerprintCache
	active    map[int]bool
}

const (
	fpName byte = iota + 1
	fpString
	fpHex
	fpInteger
	fpFloat
	fpBoolean
	fpDict
	fpStream
	fpArray
	fpNull
)

func (f *fingerprinter) object(o types.Object) (ObjectFingerprint, error) {
	if err := contextutil.Check(f.c); err != nil {
		return ObjectFingerprint{}, err
	}
	ir, ok := o.(types.IndirectRef)
	if !ok {
		return f.direct(o)
	}
	objNr := ir.ObjectNumber.Value()
	if fp, ok := f.cache[objNr]; ok {
		return fp, nil
	}
	if f.active[objNr] {
		// EqualObjects assumes pairs on a cycle are equal, which a hash cannot mirror.
		return ObjectFingerprint{}, nil
	}
	f.active[objNr] = true
	defer delete(f.active, objNr)
	d, err := f.xRefTable.Dereference(ir)
	if err != nil {
		return ObjectFingerprint{}, err
	}
	fp, err := f.direct(d)
	if err != nil {
		return ObjectFingerprint{}, err
	}
	// A subtree that reached an active ancestor is inexact for every caller, so caching it is safe.
	if f.cache != nil {
		f.cache[objNr] = fp
	}
	return fp, nil
}

func (f *fingerprinter) direct(o types.Object) (ObjectFingerprint, error) {
	h := sha256.New()
	switch v := o.(type) {
	case nil:
		// Null and dangling references, which EqualObjects treats as equal.
		h.Write([]byte{fpNull})
	case types.Name:
		writeTagged(h, fpName, []byte(v))
	case types.StringLiteral:
		writeTagged(h, fpString, []byte(v))
	case types.HexLiteral:
		writeTagged(h, fpHex, []byte(v))
	case types.Integer:
		writeTagged(h, fpInteger, binary.BigEndian.AppendUint64(nil, uint64(int64(v))))
	case types.Float:
		f := float64(v)
		if f == 0 {
			// EqualObjects compares with ==, so -0 equals +0.
			f = 0
		}
		writeTagged(h, fpFloat, binary.BigEndian.AppendUint64(nil, math.Float64bits(f)))
	case types.Boolean:
		b := byte(0)
		if v {
			b = 1
		}
		writeTagged(h, fpBoolean, []byte{b})
	case types.Dict:
		if exact, err := f.dict(h, fpDict, v); err != nil || !exact {
			return ObjectFingerprint{}, err
		}
	case types.StreamDict:
		if v.Raw == nil {
			return ObjectFingerprint{}, nil
		}
		if exact, err := f.dict(h, fpStream, v.Dict); err != nil || !exact {
			return ObjectFingerprint{}, err
		}
		raw := sha256.Sum256(v.Raw)
		h.Write(raw[:])
	case types.Array:
		writeTagged(h, fpArray, binary.BigEndian.AppendUint64(nil, uint64(len(v))))
		for _, e := range v {
			fp, err := f.object(e)
			if err != nil || !fp.Exact {
				return ObjectFingerprint{}, err
			}
			h.Write(fp.Sum[:])
		}
	default:
		// Types EqualObjects cannot compare.
		return ObjectFingerprint{}, nil
	}
	fp := ObjectFingerprint{Exact: true}
	h.Sum(fp.Sum[:0])
	return fp, nil
}

func (f *fingerprinter) dict(h hash.Hash, tag byte, d types.Dict) (bool, error) {
	if t, ok := d["Type"]; ok {
		t, err := f.xRefTable.Dereference(t)
		if err != nil {
			return false, err
		}
		if n, ok := t.(types.Name); ok && n == "Font" {
			// EqualObjects ignores subset prefixes of font names.
			return false, nil
		}
	}
	writeTagged(h, tag, binary.BigEndian.AppendUint64(nil, uint64(len(d))))
	for _, key := range slices.Sorted(maps.Keys(d)) {
		fp, err := f.object(d[key])
		if err != nil || !fp.Exact {
			return false, err
		}
		writeTagged(h, fpName, []byte(key))
		h.Write(fp.Sum[:])
	}
	return true, nil
}

func writeTagged(h hash.Hash, tag byte, b []byte) {
	h.Write([]byte{tag})
	h.Write(binary.BigEndian.AppendUint64(nil, uint64(len(b))))
	h.Write(b)
}
