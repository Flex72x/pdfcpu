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
	"context"
	"fmt"

	"github.com/pdfcpu/pdfcpu/pkg/filter"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// resolveDecodeParmsObject also handles parameter objects stored in object streams.
func resolveDecodeParmsObject(c context.Context, ctx *model.Context, obj types.Object) (types.Object, error) {
	seen := map[types.IndirectRef]bool{}
	for {
		if err := c.Err(); err != nil {
			return nil, err
		}
		ref, ok := obj.(types.IndirectRef)
		if !ok {
			return obj, nil
		}
		if seen[ref] {
			return nil, fmt.Errorf("%w: cyclic reference obj#%d", errCorruptDecodeParms, ref.ObjectNumber.Value())
		}
		seen[ref] = true
		var err error
		obj, err = dereferencedObject(c, ctx, ref.ObjectNumber.Value())
		if err != nil {
			return nil, fmt.Errorf("DecodeParms obj#%d: %w", ref.ObjectNumber.Value(), err)
		}
	}
}

// Only the filter's scalar parameters are resolved. Extension entries and
// resource references such as JBIG2Globals retain their original representation.
func resolvedFilterDecodeParms(c context.Context, ctx *model.Context, name string, obj types.Object) (types.Dict, error) {
	obj, err := resolveDecodeParmsObject(c, ctx, obj)
	if err != nil || obj == nil {
		return nil, err
	}
	dict, ok := obj.(types.Dict)
	if !ok {
		return nil, fmt.Errorf("%w: expected dictionary, got %T", errCorruptDecodeParms, obj)
	}
	if len(dict) == 0 {
		return nil, nil
	}
	resolved := dict.Clone().(types.Dict)
	var integers, booleans []string
	switch name {
	case filter.Flate, filter.LZW:
		integers = []string{"Predictor", "Colors", "BitsPerComponent", "Columns"}
		if name == filter.LZW {
			integers = append(integers, "EarlyChange")
		}
	case filter.CCITTFax:
		integers = []string{"K", "Columns", "Rows", "DamagedRowsBeforeError"}
		booleans = []string{"EndOfLine", "EncodedByteAlign", "EndOfBlock", "BlackIs1"}
	}
	for group, keys := range [][]string{integers, booleans} {
		for _, key := range keys {
			value, found := dict.Find(key)
			if !found {
				continue
			}
			value, err := resolveDecodeParmsObject(c, ctx, value)
			if err != nil {
				return nil, fmt.Errorf("%s parameter %s: %w", name, key, err)
			}
			if value != nil {
				if group == 0 {
					if _, ok := value.(types.Integer); !ok {
						return nil, fmt.Errorf("%s parameter %s: %w: expected integer, got %T", name, key, errCorruptDecodeParms, value)
					}
				} else if _, ok := value.(types.Boolean); !ok {
					return nil, fmt.Errorf("%s parameter %s: %w: expected boolean, got %T", name, key, errCorruptDecodeParms, value)
				}
			}
			resolved[key] = value
		}
	}
	return resolved, nil
}
