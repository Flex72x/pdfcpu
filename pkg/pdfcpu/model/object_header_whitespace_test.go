package model

import (
	"fmt"
	"testing"
)

// TestParseObjectAttributesWhitespace checks PDF separators keep object and generation numbers distinct.
func TestParseObjectAttributesWhitespace(t *testing.T) {
	whitespace := []string{"\x00", "\t", "\n", "\f", "\r", " ", "\r\n", "\x00\t\n\f\r "}
	const remainder = " << /Length 0 >>\nendobj\n13 0 obj"
	for _, beforeGeneration := range whitespace {
		for _, beforeObj := range whitespace {
			for _, generation := range []int{0, 34} {
				t.Run(fmt.Sprintf("%q/%q/generation=%d", beforeGeneration, beforeObj, generation), func(t *testing.T) {
					line := fmt.Sprintf("%s12%s%d%sobj%s", beforeGeneration, beforeGeneration, generation, beforeObj, remainder)
					objNr, genNr, err := ParseObjectAttributes(&line)
					if err != nil {
						t.Fatal(err)
					}
					if objNr == nil || genNr == nil {
						t.Fatal("missing object or generation number")
					}
					if *objNr != 12 || *genNr != generation {
						t.Fatalf("got %d %d, want 12 %d", *objNr, *genNr, generation)
					}
					if line != remainder {
						t.Fatalf("remaining data = %q, want %q", line, remainder)
					}
				})
			}
		}
	}
}
