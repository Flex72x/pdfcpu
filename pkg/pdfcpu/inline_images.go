package pdfcpu

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pdfcpu/pdfcpu/internal/contextutil"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

var errInlineImagePreserved = errors.New("inline image cannot be externalized safely")

type inlineImageSpan struct {
	start, end int
	image      types.StreamDict
}

type inlineImageContent struct {
	images []inlineImageSpan
	forms  []string
}

type inlineImageNormalizer struct {
	ctx      context.Context
	doc      *model.Context
	forms    map[int]bool
	count    int
	optimize bool
}

func ExternalizeInlineImages(ctx context.Context, doc *model.Context) (int, error) {
	return externalizeInlineImages(ctx, doc, false)
}

func externalizeInlineImages(ctx context.Context, doc *model.Context, optimize bool) (int, error) {
	if err := contextutil.Check(ctx); err != nil {
		return 0, err
	}
	if err := requireContextWithXRefTable(doc); err != nil {
		return 0, err
	}
	normalizer := inlineImageNormalizer{ctx: ctx, doc: doc, forms: map[int]bool{}, optimize: optimize}
	if err := doc.TransformPageContents(ctx, normalizer.pageContent, optimize && shouldOptimizeResourceDicts(doc)); err != nil {
		return 0, err
	}
	return normalizer.count, contextutil.Check(ctx)
}

func (normalizer *inlineImageNormalizer) pageContent(data *model.PageContentData) error {
	if data.DecodeError != nil {
		if normalizer.optimize && normalizer.doc.OptimizeDuplicateContentStreams {
			return data.DecodeError
		}
		return nil
	}
	rewritten, local, changed, err := normalizer.content(data.Content, data.Resources, 0)
	if err != nil {
		return err
	}
	if changed {
		reference, err := normalizer.doc.StreamDictIndRef(rewritten)
		if err != nil {
			return err
		}
		data.Dict["Contents"] = *reference
		data.Content, data.Resources = rewritten, local
	}
	if normalizer.optimize && normalizer.doc.OptimizeDuplicateContentStreams {
		if err := optimizePageContent(normalizer.ctx, normalizer.doc, data.Dict, data.EmptyStreams); err != nil {
			return fmt.Errorf("optimize content: %w", err)
		}
	}
	return nil
}

func (normalizer *inlineImageNormalizer) formResources(resources types.Dict, names []string, depth int) error {
	if len(names) == 0 {
		return nil
	}
	objects, err := normalizer.doc.DereferenceDict(resources["XObject"])
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := contextutil.Check(normalizer.ctx); err != nil {
			return err
		}
		object, _ := objects.Find(name)
		reference, ok := object.(types.IndirectRef)
		if !ok || normalizer.forms[reference.ObjectNumber.Value()] {
			continue
		}
		normalizer.forms[reference.ObjectNumber.Value()] = true
		stream, _, err := normalizer.doc.DereferenceStreamDict(reference)
		if err != nil {
			return err
		}
		if stream == nil {
			continue
		}
		subtype, _, err := normalizer.doc.DereferenceNameEntry(stream.Dict, "Subtype")
		if err != nil {
			return err
		}
		if subtype == nil || *subtype != "Form" || stream.Dict["Resources"] == nil {
			continue
		}
		if err := normalizer.doc.CheckRecursionDepth("inline image forms", depth+1); err != nil {
			return err
		}
		local, err := normalizer.doc.DereferenceDict(stream.Dict["Resources"])
		if err != nil {
			return err
		}
		var decoded bytes.Buffer
		if _, err := stream.WriteDecodedTo(normalizer.ctx, &decoded, normalizer.doc.Limits.MaxDecodeBytes); err != nil {
			if contextErr := normalizer.ctx.Err(); contextErr != nil {
				return contextErr
			}
			if errors.Is(err, types.ErrEncodedStreamSource) {
				return err
			}
			continue
		}
		rewritten, updatedResources, changed, err := normalizer.content(decoded.Bytes(), local, depth+1)
		if err != nil {
			return err
		}
		if !changed {
			continue
		}
		updated := stream.Clone().(types.StreamDict)
		updated.Content = rewritten
		updated.RawSource = nil
		updated.Dict["Resources"] = updatedResources
		if err := updated.Encode(); err != nil {
			return err
		}
		normalizer.doc.Table[reference.ObjectNumber.Value()].Object = updated
	}
	return nil
}

func (normalizer *inlineImageNormalizer) content(content []byte, resources types.Dict, depth int) ([]byte, types.Dict, bool, error) {
	if !bytes.Contains(content, []byte("BI")) && !bytes.Contains(content, []byte("Do")) {
		return nil, nil, false, nil
	}
	analysis, err := scanInlineImages(normalizer.ctx, normalizer.doc, content, resources)
	if err != nil {
		if contextErr := normalizer.ctx.Err(); contextErr != nil {
			return nil, nil, false, contextErr
		}
		if errors.Is(err, errInlineImagePreserved) {
			return nil, nil, false, nil
		}
		return nil, nil, false, err
	}
	if err := normalizer.formResources(resources, analysis.forms, depth); err != nil {
		return nil, nil, false, err
	}
	spans := analysis.images
	if len(spans) == 0 || (normalizer.doc.Limits.MaxObjectCount > 0 && int64(*normalizer.doc.Size)+int64(len(spans))+1 > int64(normalizer.doc.Limits.MaxObjectCount)) {
		return nil, nil, false, nil
	}
	local := types.NewDict()
	for name, value := range resources {
		local[name] = value
	}
	objects, err := normalizer.doc.DereferenceDict(local["XObject"])
	if err != nil {
		return nil, nil, false, err
	}
	clonedObjects := types.NewDict()
	for name, value := range objects {
		clonedObjects[name] = value
	}
	local["XObject"] = clonedObjects
	var result bytes.Buffer
	previous := 0
	for _, span := range spans {
		if err := contextutil.Check(normalizer.ctx); err != nil {
			return nil, nil, false, err
		}
		reference, err := normalizer.doc.IndRefForNewObject(span.image)
		if err != nil {
			return nil, nil, false, err
		}
		name := fmt.Sprintf("Inline%d", reference.ObjectNumber.Value())
		for suffix := 1; clonedObjects[name] != nil; suffix++ {
			name = fmt.Sprintf("Inline%d_%d", reference.ObjectNumber.Value(), suffix)
		}
		clonedObjects[name] = *reference
		result.Write(content[previous:span.start])
		fmt.Fprintf(&result, " /%s Do ", name)
		previous = span.end
	}
	result.Write(content[previous:])
	normalizer.count += len(spans)
	return result.Bytes(), local, true, nil
}

func inlineWhitespace(value byte) bool {
	return value == 0 || value == 9 || value == 10 || value == 12 || value == 13 || value == 32
}

func inlineDelimiter(value byte) bool {
	return inlineWhitespace(value) || strings.ContainsRune("()<>[]{}/%", rune(value))
}

func inlineSkipSpace(input string) string {
	for {
		input = strings.TrimLeftFunc(input, func(value rune) bool { return value <= 255 && inlineWhitespace(byte(value)) })
		if len(input) == 0 || input[0] != '%' {
			return input
		}
		end := strings.IndexAny(input, "\r\n")
		if end < 0 {
			return ""
		}
		input = input[end:]
	}
}

func scanInlineImages(ctx context.Context, doc *model.Context, content []byte, resources types.Dict) (inlineImageContent, error) {
	input := string(content)
	var result inlineImageContent
	var operand types.Name
	var forms types.StringSet
	for {
		if err := contextutil.Check(ctx); err != nil {
			return inlineImageContent{}, err
		}
		input = inlineSkipSpace(input)
		if len(input) == 0 {
			return result, nil
		}
		start := len(content) - len(input)
		if strings.ContainsRune("/<([", rune(input[0])) {
			object, err := model.ParseObject(ctx, &input, 0, doc.MaxRecursionDepth())
			if err != nil {
				return inlineImageContent{}, errInlineImagePreserved
			}
			operand, _ = object.(types.Name)
			continue
		}
		end := 0
		for end < len(input) && !inlineDelimiter(input[end]) {
			end++
		}
		if end == 0 {
			return inlineImageContent{}, errInlineImagePreserved
		}
		token := input[:end]
		input = input[end:]
		if token == "Do" && operand != "" {
			name := operand.Value()
			if forms == nil {
				forms = types.StringSet{}
			}
			if !forms[name] {
				forms[name] = true
				result.forms = append(result.forms, name)
			}
		}
		operand = ""
		if token != "BI" {
			continue
		}
		if doc.Limits.MaxObjectCount > 0 && int64(*doc.Size)+int64(len(result.images))+2 > int64(doc.Limits.MaxObjectCount) {
			return inlineImageContent{}, errInlineImagePreserved
		}
		image, err := readInlineImage(ctx, doc, &input, resources)
		if err != nil {
			return inlineImageContent{}, err
		}
		result.images = append(result.images, inlineImageSpan{start: start, end: len(content) - len(input), image: image})
	}
}
