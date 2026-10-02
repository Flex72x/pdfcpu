package pdfcpu

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/pdfcpu/pdfcpu/internal/contextutil"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// SpillImageStreams moves the current encoded image representations to owned
// files after reading/optimization. It releases both xref and optimization
// cache references, including masks/duplicates, without changing sample data.
// The caller owns directory and keeps it alive through decoding and writing.
// This does not make the initial PDF parser lazy or remove its resource limits.
func SpillImageStreams(c context.Context, doc *model.Context, directory string) (int64, error) {
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
	var total int64
	for _, number := range keys {
		if err := contextutil.Check(c); err != nil {
			return total, err
		}
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
		path := filepath.Join(directory, fmt.Sprintf("original-stream-%d.bin", number))
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return total, err
		}
		raw, err := sd.OpenRaw()
		if err == nil {
			_, err = io.CopyBuffer(contextutil.Writer(c, file), contextutil.Reader(c, raw), make([]byte, 32<<10))
			err = errors.Join(err, raw.Close())
		}
		err = errors.Join(err, file.Close(), contextutil.Check(c))
		if err != nil {
			return total, errors.Join(err, os.Remove(path))
		}
		source, err := types.NewFileStreamSource(path)
		if err != nil {
			return total, err
		}
		length := sd.RawLength()
		if source.Size() != length {
			return total, fmt.Errorf("PDF image %d backing length mismatch", number)
		}
		if err := sd.SetRawSource(source); err != nil {
			return total, err
		}
		entry.Object = sd
		if doc.Optimize != nil {
			if image := doc.Optimize.ImageObjects[number]; image != nil && image.ImageDict != nil {
				*image.ImageDict = sd
			}
			if image := doc.Optimize.DuplicateImages[number]; image != nil && image.ImageDict != nil {
				*image.ImageDict = sd
			}
		}
		total += length
	}
	return total, nil
}
