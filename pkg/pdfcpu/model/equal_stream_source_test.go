package model

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestEqualObjectsSupportsEncodedStreamSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stream")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := types.NewFileStreamSource(path)
	if err != nil {
		t.Fatal(err)
	}
	backed := types.StreamDict{Dict: types.Dict{"Length": types.Integer(3)}, RawSource: source}
	xref := newXRefTable(NewStatelessConfiguration())
	for _, test := range []struct {
		name string
		raw  string
		want bool
	}{
		{"same", "abc", true},
		{"different", "abd", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			memory := types.StreamDict{Dict: types.Dict{"Length": types.Integer(3)}, Raw: []byte(test.raw)}
			for _, pair := range [][2]types.StreamDict{{backed, memory}, {memory, backed}} {
				equal, err := EqualObjects(t.Context(), pair[0], pair[1], xref, nil)
				if err != nil || equal != test.want {
					t.Fatalf("equal=%t want=%t err=%v", equal, test.want, err)
				}
			}
		})
	}
	if equal, err := EqualObjects(t.Context(), backed, backed, xref, nil); !equal || err != nil {
		t.Fatalf("same source: equal=%t err=%v", equal, err)
	}
	for _, pair := range [][2]types.StreamDict{{backed, {Dict: backed.Dict}}, {{Dict: backed.Dict}, backed}} {
		if equal, err := EqualObjects(t.Context(), pair[0], pair[1], xref, nil); equal || err == nil {
			t.Fatalf("unloaded stream accepted: equal=%t err=%v", equal, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if equal, err := EqualObjects(ctx, backed, backed, xref, nil); equal || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: equal=%t err=%v", equal, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if equal, err := EqualObjects(t.Context(), backed, backed, xref, nil); equal || !errors.Is(err, os.ErrNotExist) || !errors.Is(err, types.ErrEncodedStreamSource) {
		t.Fatalf("source error: equal=%t err=%v", equal, err)
	}
}
