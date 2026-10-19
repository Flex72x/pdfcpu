package pdfcpu

import (
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/filter"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

type inlineFormSource struct {
	data  []byte
	open  func() error
	reads int
}

func (source *inlineFormSource) Size() int64 { return int64(len(source.data)) }

func (source *inlineFormSource) Open() (io.ReadCloser, error) {
	source.reads++
	if source.open != nil {
		if err := source.open(); err != nil {
			return nil, err
		}
	}
	return io.NopCloser(strings.NewReader(string(source.data))), nil
}

func TestExternalizeInlineImagesOnlyInvokedForms(t *testing.T) {
	for _, content := range []string{
		"q Q",
		"% /Unused Do\n(/Unused Do) Tj /Span <</ActualText (/Unused Do)>> BDC EMC",
		"BI /W 10 /H 1 /CS /G /BPC 8 ID /Unused Do EI Q",
		"/Unused q Do",
	} {
		t.Run(content, func(t *testing.T) {
			doc, page := inlineTestDocument(t, content)
			source := &inlineFormSource{data: []byte("unread"), open: func() error { return errors.New("unused form opened") }}
			form := types.StreamDict{Dict: types.Dict{
				"Type": types.Name("XObject"), "Subtype": types.Name("Form"), "Resources": types.NewDict(),
			}}
			if err := form.SetRawSource(source); err != nil {
				t.Fatal(err)
			}
			reference, err := doc.IndRefForNewObject(form)
			if err != nil {
				t.Fatal(err)
			}
			page["Resources"] = types.Dict{"XObject": types.Dict{"Unused": *reference}}
			if _, err := ExternalizeInlineImages(t.Context(), doc); err != nil {
				t.Fatal(err)
			}
			if source.reads != 0 {
				t.Fatalf("unused form opened %d times", source.reads)
			}
		})
	}
}

func TestExternalizeInlineImagesEscapedFormNames(t *testing.T) {
	doc, page := inlineTestDocument(t, "/Used#20Form Do /Used#20Form Do")
	source := &inlineFormSource{data: []byte("BI /W 1 /H 1 /CS /G /BPC 8 ID x EI Q")}
	form := types.StreamDict{Dict: types.Dict{"Type": types.Name("XObject"), "Subtype": types.Name("Form"), "Resources": types.NewDict()}}
	if err := form.SetRawSource(source); err != nil {
		t.Fatal(err)
	}
	reference, err := doc.IndRefForNewObject(form)
	if err != nil {
		t.Fatal(err)
	}
	page["Resources"] = types.Dict{"XObject": types.Dict{"Used#20Form": *reference}}
	count, err := ExternalizeInlineImages(t.Context(), doc)
	if err != nil || count != 1 || source.reads != 1 {
		t.Fatalf("count=%d, reads=%d, err=%v", count, source.reads, err)
	}
}

func TestExternalizeInlineImagesFormDecodeBackingParity(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		for _, backed := range []bool{false, true} {
			t.Run(fmt.Sprintf("corrupt=%t/backed=%t", corrupt, backed), func(t *testing.T) {
				doc, page := inlineTestDocument(t, "/Form Do")
				doc.Limits.MaxDecodeBytes = 64
				encoded := sourceTestFlate(t, []byte(strings.Repeat("q Q\n", 1024)))
				if corrupt {
					encoded = []byte("invalid flate")
				}
				form := types.StreamDict{Dict: types.Dict{"Type": types.Name("XObject"), "Subtype": types.Name("Form"), "Resources": types.NewDict()}, Raw: encoded, FilterPipeline: []types.PDFFilter{{Name: filter.Flate}}}
				if backed {
					if err := form.SetRawSource(&inlineFormSource{data: encoded}); err != nil {
						t.Fatal(err)
					}
				}
				reference, err := doc.IndRefForNewObject(form)
				if err != nil {
					t.Fatal(err)
				}
				page["Resources"] = types.Dict{"XObject": types.Dict{"Form": *reference}}
				if count, err := ExternalizeInlineImages(t.Context(), doc); err != nil || count != 0 {
					t.Fatalf("count=%d, err=%v", count, err)
				}
			})
		}
	}
}

func TestExternalizeInlineImagesCancellationDuringFormDecode(t *testing.T) {
	for _, content := range []string{"q Q", "BI /W 1 /H 1 /CS /G /BPC 8 ID x EI Q"} {
		t.Run(content, func(t *testing.T) {
			doc, page := inlineTestDocument(t, "/Form Do")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			source := &inlineFormSource{data: []byte(content), open: func() error { cancel(); return nil }}
			form := types.StreamDict{Dict: types.Dict{"Type": types.Name("XObject"), "Subtype": types.Name("Form"), "Resources": types.NewDict()}}
			if err := form.SetRawSource(source); err != nil {
				t.Fatal(err)
			}
			reference, err := doc.IndRefForNewObject(form)
			if err != nil {
				t.Fatal(err)
			}
			page["Resources"] = types.Dict{"XObject": types.Dict{"Form": *reference}}
			if _, err := ExternalizeInlineImages(ctx, doc); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		})
	}
}

func TestOptimizeXRefTableDecodesPageOnce(t *testing.T) {
	for _, duplicateStreams := range []bool{false, true} {
		for _, array := range []bool{false, true} {
			t.Run(fmt.Sprintf("duplicate=%t/array=%t", duplicateStreams, array), func(t *testing.T) {
				doc, page := inlineTestDocument(t, "")
				doc.Optimize = testOptimizeContext(t).Optimize
				doc.Cmd = model.OPTIMIZE
				doc.OptimizeResourceDicts = true
				doc.OptimizeDuplicateContentStreams = duplicateStreams
				var sources []*inlineFormSource
				var references types.Array
				for _, content := range []string{"", "q Q"} {
					source := &inlineFormSource{data: sourceTestFlate(t, []byte(content))}
					stream := types.StreamDict{Dict: types.NewDict(), FilterPipeline: []types.PDFFilter{{Name: filter.Flate}}}
					if err := stream.SetRawSource(source); err != nil {
						t.Fatal(err)
					}
					reference, err := doc.IndRefForNewObject(stream)
					if err != nil {
						t.Fatal(err)
					}
					references = append(references, *reference)
					sources = append(sources, source)
				}
				if array {
					page["Contents"] = references
				} else {
					page["Contents"] = references[1]
				}
				if err := OptimizeXRefTable(t.Context(), doc); err != nil {
					t.Fatal(err)
				}
				for index, source := range sources {
					want := 1
					if !array && index == 0 {
						want = 0
					}
					if source.reads != want {
						t.Fatalf("stream %d opened %d times, want %d", index, source.reads, want)
					}
					stream, _, err := doc.DereferenceStreamDict(references[index])
					if err != nil {
						t.Fatal(err)
					}
					if stream.Content != nil {
						t.Fatal("page decode retained after processing")
					}
				}
				if duplicateStreams && array && len(page["Contents"].(types.Array)) != 1 {
					t.Fatal("empty stream retained")
				}
			})
		}
	}
}

func TestOptimizeXRefTableLocalResourcesShadowInheritedDefaults(t *testing.T) {
	doc, page := inlineTestDocument(t, "BI /W 1 /H 1 /CS /RGB /BPC 8 ID abc EI Q")
	doc.Optimize = testOptimizeContext(t).Optimize
	doc.Cmd = model.OPTIMIZE
	doc.OptimizeResourceDicts = true
	root, err := doc.Pages()
	if err != nil {
		t.Fatal(err)
	}
	pages, err := doc.DereferenceDict(*root)
	if err != nil {
		t.Fatal(err)
	}
	pages["Resources"] = types.Dict{"ColorSpace": types.Dict{"DefaultRGB": types.Array{types.Name(model.CalRGBCS), types.Dict{"Gamma": types.NewIntegerArray(4, 4, 4)}}}}
	page["Resources"] = types.NewDict()
	if err := OptimizeXRefTable(t.Context(), doc); err != nil {
		t.Fatal(err)
	}
	for _, reference := range page.DictEntry("Resources").DictEntry("XObject") {
		stream, _, err := doc.DereferenceStreamDict(reference)
		if err != nil {
			t.Fatal(err)
		}
		if stream.Dict["ColorSpace"] != types.Name(model.DeviceRGBCS) {
			t.Fatalf("ancestor default leaked into local scope: %v", stream.Dict["ColorSpace"])
		}
	}
	if page.DictEntry("Resources")["ColorSpace"] != nil {
		t.Fatal("ancestor color resources merged into local scope")
	}
}

func TestOptimizeXRefTableFileBackedContentDeduplication(t *testing.T) {
	for _, identical := range []bool{false, true} {
		t.Run(fmt.Sprint(identical), func(t *testing.T) {
			doc, firstPage := inlineTestDocument(t, "")
			doc.Optimize = testOptimizeContext(t).Optimize
			doc.Cmd = model.OPTIMIZE
			doc.OptimizeDuplicateContentStreams = true
			root, err := doc.Pages()
			if err != nil {
				t.Fatal(err)
			}
			pages, err := doc.DereferenceDict(*root)
			if err != nil {
				t.Fatal(err)
			}
			secondReference, err := doc.EmptyPage(root, types.RectForFormat("A4"), 0)
			if err != nil {
				t.Fatal(err)
			}
			if err := model.AppendPageTree(secondReference, 1, pages); err != nil {
				t.Fatal(err)
			}
			secondPage, err := doc.DereferenceDict(*secondReference)
			if err != nil {
				t.Fatal(err)
			}
			doc.PageCount = 2
			for index, page := range []types.Dict{firstPage, secondPage} {
				content := "0 g 0 0 1 1 re f"
				if index == 1 && !identical {
					content = "1 g 0 0 1 1 re f"
				}
				stream := types.StreamDict{Dict: types.NewDict()}
				if err := stream.SetRawSource(&inlineFormSource{data: []byte(content)}); err != nil {
					t.Fatal(err)
				}
				reference, err := doc.IndRefForNewObject(stream)
				if err != nil {
					t.Fatal(err)
				}
				page["Contents"] = *reference
			}
			if err := OptimizeXRefTable(t.Context(), doc); err != nil {
				t.Fatal(err)
			}
			sameReference := firstPage["Contents"] == secondPage["Contents"]
			if sameReference != identical {
				t.Fatalf("content deduplication=%t, want %t", sameReference, identical)
			}
		})
	}
}

func TestExternalizeInlineImagesNullResourcesInherit(t *testing.T) {
	for _, indirect := range []bool{false, true} {
		t.Run(fmt.Sprint(indirect), func(t *testing.T) {
			doc, page := inlineTestDocument(t, "BI /W 1 /H 1 /CS /RGB /BPC 8 ID abc EI Q")
			root, err := doc.Pages()
			if err != nil {
				t.Fatal(err)
			}
			pages, err := doc.DereferenceDict(*root)
			if err != nil {
				t.Fatal(err)
			}
			color := types.Array{types.Name(model.CalRGBCS), types.Dict{"Gamma": types.NewIntegerArray(4, 4, 4)}}
			pages["Resources"] = types.Dict{"ColorSpace": types.Dict{"DefaultRGB": color}}
			page["Resources"] = nil
			if indirect {
				reference, err := doc.IndRefForNewObject(nil)
				if err != nil {
					t.Fatal(err)
				}
				page["Resources"] = *reference
			}
			_, _, inherited, err := doc.PageDict(t.Context(), 1, false)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(inherited.Resources.DictEntry("ColorSpace")["DefaultRGB"], color) {
				t.Fatal("raw page lookup lost null resource inheritance")
			}
			count, err := ExternalizeInlineImages(t.Context(), doc)
			if err != nil || count != 1 {
				t.Fatalf("count=%d, err=%v", count, err)
			}
			for _, reference := range page.DictEntry("Resources").DictEntry("XObject") {
				stream, _, err := doc.DereferenceStreamDict(reference)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(stream.Dict["ColorSpace"], color) {
					t.Fatal("null resource dictionary blocked inheritance")
				}
			}
		})
	}
}

func TestOptimizeXRefTableDoesNotRemoveCorruptContent(t *testing.T) {
	for _, duplicateStreams := range []bool{false, true} {
		t.Run(fmt.Sprint(duplicateStreams), func(t *testing.T) {
			doc, page := inlineTestDocument(t, "")
			doc.Optimize = testOptimizeContext(t).Optimize
			doc.Cmd = model.OPTIMIZE
			doc.OptimizeDuplicateContentStreams = duplicateStreams
			doc.XRefTable.ValidationMode = model.ValidationRelaxed
			encoded := sourceTestFlate(t, []byte("q Q"))
			encoded[len(encoded)-1] ^= 0xff
			stream := types.StreamDict{Dict: types.NewDict(), Raw: encoded, FilterPipeline: []types.PDFFilter{{Name: filter.Flate}}}
			reference, err := doc.IndRefForNewObject(stream)
			if err != nil {
				t.Fatal(err)
			}
			page["Contents"] = *reference
			err = OptimizeXRefTable(t.Context(), doc)
			if duplicateStreams && !errors.Is(err, zlib.ErrChecksum) {
				t.Fatalf("checksum cause lost: %v", err)
			}
			if !duplicateStreams && err != nil {
				t.Fatal(err)
			}
			if page["Contents"] != *reference {
				t.Fatal("corrupt nonempty stream removed or rewritten")
			}
		})
	}
}
