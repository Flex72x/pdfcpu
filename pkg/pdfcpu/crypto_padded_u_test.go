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
	"fmt"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func aes256PasswordContext(t *testing.T, revision int, userPW, ownerPW string) (*model.Context, types.Dict) {
	t.Helper()
	conf := model.NewStatelessConfiguration()
	conf.Offline = true
	ctx, err := model.NewContext(bytes.NewReader(nil), conf)
	if err != nil {
		t.Fatal(err)
	}
	version := model.V17
	ctx.HeaderVersion = &version
	ctx.UserPW, ctx.OwnerPW = userPW, ownerPW
	dict := newEncryptDict(false, true, 256, -4)
	dict["R"] = types.Integer(revision)
	ctx.E, err = supportedEncryption(ctx, dict)
	if err != nil {
		t.Fatal(err)
	}
	ctx.E.ID = bytes.Repeat([]byte{1}, 16)
	return ctx, dict
}

func checkAES256Password(t *testing.T, ctx *model.Context, owner bool, password string, wantKey []byte) {
	t.Helper()
	ctx.EncKey = nil
	var accepted bool
	var err error
	if owner {
		ctx.OwnerPW = password
		accepted, err = validateOwnerPassword(ctx)
	} else {
		ctx.UserPW = password
		accepted, err = validateUserPassword(ctx)
	}
	if err != nil || !accepted {
		t.Fatalf("password rejected: ok=%t err=%v", accepted, err)
	}
	if !bytes.Equal(ctx.EncKey, wantKey) {
		t.Fatal("password did not recover the original file encryption key")
	}
}

// TestAES256OwnerPasswordWithPaddedU checks that excess U bytes do not affect owner authentication.
func TestAES256OwnerPasswordWithPaddedU(t *testing.T) {
	for _, revision := range []int{5, 6} {
		for _, extraBytes := range []int{1, 79} {
			t.Run(fmt.Sprintf("R%d/U=%d", revision, 48+extraBytes), func(t *testing.T) {
				ctx, d := aes256PasswordContext(t, revision, "user", "owner")
				if err := calcOAndU(ctx, d); err != nil {
					t.Fatal(err)
				}
				wantKey := bytes.Clone(ctx.EncKey)
				paddedU := append(bytes.Clone(ctx.E.U), bytes.Repeat([]byte{0xa5}, extraBytes)...)
				d.Update("U", types.NewHexLiteral(paddedU))
				var err error
				ctx.E, err = supportedEncryption(ctx, d)
				if err != nil {
					t.Fatal(err)
				}
				checkAES256Password(t, ctx, false, "user", wantKey)
				checkAES256Password(t, ctx, true, "owner", wantKey)
			})
		}
	}
}
