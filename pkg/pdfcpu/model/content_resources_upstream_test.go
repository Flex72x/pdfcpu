package model

import (
	"maps"
	"testing"
)

func checkContentResources(t *testing.T, content string, want map[string][]string) {
	t.Helper()
	got, err := parseContent(content)
	if err != nil {
		t.Fatal(err)
	}
	expected := NewPageResourceNames()
	for kind, names := range want {
		for _, name := range names {
			expected[kind][name] = true
		}
	}
	for kind, names := range expected {
		if !maps.Equal(got[kind], names) {
			t.Errorf("%s resources = %v, want %v", kind, got[kind], names)
		}
	}
}

func TestContentResourceNameEscapes(t *testing.T) {
	checkContentResources(t, `/Im#201 Do /F#31 12 Tf /CS#30 cs /GS#31 gs /P#31 scn /SH#31 sh /Span /MC#31 BDC`,
		map[string][]string{
			"XObject": {"Im 1"}, "Font": {"F1"}, "ColorSpace": {"CS0"}, "ExtGState": {"GS1"},
			"Pattern": {"P1"}, "Shading": {"SH1"}, "Properties": {"MC1"},
		})
	checkContentResources(t, `/Im#2f#23 Do /#44eviceRGB cs`, map[string][]string{"XObject": {"Im/#"}})
	checkContentResources(t, "BI /CS /CS#300 ID x EI Q", map[string][]string{"ColorSpace": {"CS00"}})
}

func TestInlineImageColorSpaceArrays(t *testing.T) {
	for _, cs := range []string{
		`[/I /RGB 1 <000000FFFFFF>]`,
		`[/Indexed /DeviceRGB 1 (ab(cd))]`,
		`[/#49 /#52GB 1 (]/IDxx)]`,
	} {
		t.Run(cs, func(t *testing.T) {
			checkContentResources(t, "BI /CS "+cs+" ID x EI Q /Im1 Do", map[string][]string{"XObject": {"Im1"}})
		})
	}
	// Preserve a resource reference even in the tolerated nonstandard named-base form.
	checkContentResources(t, `BI /CS [/Indexed /CS#30 1 (]/IDxx)] ID x EI Q /Im1 Do`,
		map[string][]string{"ColorSpace": {"CS0"}, "XObject": {"Im1"}})
}

// TestContentNestedStringsAndDictionaries checks string contents never become operators or delimiters.
func TestContentNestedStringsAndDictionaries(t *testing.T) {
	for _, content := range []string{
		`(a(b)/Fake Do) Tj`,
		`[(a(b)/Fake Do) 10 (c)] TJ`,
		`(a\)b(c)/Fake Do) Tj`,
		`(a\\) Tj`,
		`/Span <</ActualText (<<)>> BDC`,
		`/Span <</ActualText (>> /Fake Do)>> BDC`,
		`/Span <</ActualText <3E3E>>> BDC`,
		`/Span <</Nested [<< /Text (a(b)<<>>) >>]>> BDC`,
		"/Span << % >> ( <<\n/ActualText (text)>> BDC",
	} {
		t.Run(content, func(t *testing.T) {
			checkContentResources(t, content+" /F1 12 Tf /Im1 Do",
				map[string][]string{"Font": {"F1"}, "XObject": {"Im1"}})
		})
	}
}

func TestContentResourceInvalidEscapesRemainTolerated(t *testing.T) {
	for _, name := range []string{"Im#", "Im#GG", "Im#00"} {
		checkContentResources(t, "/"+name+" Do", map[string][]string{"XObject": {name}})
	}
}
