package pdfcpu

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/pdfcpu/pdfcpu/internal/contextutil"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// imageStreamsFile is the single backing file SpillImageStreams writes into
// its directory: one file per stream would cost a create and close for each
// of possibly hundreds of thousands of small images.
const imageStreamsFile = "original-image-streams.bin"

// SpillImageStreams moves the current encoded image representations to one
// owned file after reading/optimization. It releases both xref and
// optimization cache references, including masks/duplicates, without changing
// sample data. The document switches to the file only after every stream was
// written, so a failure leaves it unchanged. The caller owns directory and
// keeps it alive through decoding and writing. This does not make the initial
// PDF parser lazy or remove its resource limits.
func SpillImageStreams(c context.Context, doc *model.Context, directory string) (_ int64, err error) {
	if err := contextutil.Check(c); err != nil {
		return 0, err
	}
	if err := requireContextWithXRefTable(doc); err != nil {
		return 0, err
	}
	keys := make([]int, 0, len(doc.Table))
	for number := range doc.Table {
		keys = append(keys, number)
	}
	sort.Ints(keys)
	type spilledStream struct {
		number         int
		sd             types.StreamDict
		offset, length int64
	}
	var streams []spilledStream
	for _, number := range keys {
		entry := doc.Table[number]
		if entry == nil || entry.Object == nil {
			continue
		}
		var sd types.StreamDict
		switch value := entry.Object.(type) {
		case types.StreamDict:
			sd = value
		case *types.StreamDict:
			sd = *value
		default:
			continue
		}
		if !sd.Image() || sd.RawSource != nil {
			continue
		}
		streams = append(streams, spilledStream{number: number, sd: sd})
	}
	if len(streams) == 0 {
		return 0, nil
	}

	path := filepath.Join(directory, imageStreamsFile)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Remove(path))
		}
	}()
	writer := bufio.NewWriterSize(contextutil.Writer(c, file), 1<<20)
	var total int64
	for i := range streams {
		s := &streams[i]
		s.offset, s.length = total, s.sd.RawLength()
		n, writeErr := s.sd.WriteRawTo(writer)
		if writeErr == nil && n != s.length {
			writeErr = fmt.Errorf("PDF image %d backing length mismatch", s.number)
		}
		if writeErr != nil {
			return 0, errors.Join(writeErr, file.Close())
		}
		total += n
	}
	if err = errors.Join(writer.Flush(), file.Close(), contextutil.Check(c)); err != nil {
		return 0, err
	}

	sections, err := types.OpenFileSections(path)
	if err != nil {
		return 0, err
	}
	sources := make([]types.EncodedStreamSource, len(streams))
	for i, s := range streams {
		if sources[i], err = sections.Section(s.offset, s.length); err != nil {
			return 0, err
		}
	}
	for i := range streams {
		s := &streams[i]
		if err = s.sd.SetRawSource(sources[i]); err != nil {
			return 0, err
		}
		doc.Table[s.number].Object = s.sd
		if doc.Optimize != nil {
			if image := doc.Optimize.ImageObjects[s.number]; image != nil && image.ImageDict != nil {
				*image.ImageDict = s.sd
			}
			if image := doc.Optimize.DuplicateImages[s.number]; image != nil && image.ImageDict != nil {
				*image.ImageDict = s.sd
			}
		}
	}
	return total, nil
}
