package pdfcpu

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/pdfcpu/pdfcpu/internal/contextutil"
	"github.com/pdfcpu/pdfcpu/pkg/filter"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// WriteEncodedImageSource removes only filters preceding the terminal image
// codec, matching ExtractEncodedImageSource. A sole DCT/JPX stream copies
// directly from its backing without creating an encoded byte buffer. Outer
// filters retain their existing bounded decoder. Samples/profiles/masks remain
// owned by the PDF dictionary; this function does not render the image.
func WriteEncodedImageSource(c context.Context, doc *model.Context, sd *types.StreamDict, writer io.Writer) (ImageSourceKind, bool, error) {
	if err := contextutil.Check(c); err != nil {
		return 0, false, err
	}
	if err := requireContextWithXRefTable(doc); err != nil {
		return 0, false, err
	}
	if err := requireStreamDict(sd); err != nil {
		return 0, false, err
	}
	if writer == nil {
		return 0, false, fmt.Errorf("missing encoded image source writer")
	}
	if len(sd.FilterPipeline) != 1 || sd.FilterPipeline[0].Name == filter.JBIG2 {
		source, handled, err := ExtractEncodedImageSource(c, doc, sd)
		if !handled || err != nil {
			return 0, handled, err
		}
		_, err = io.CopyBuffer(contextutil.Writer(c, writer), contextutil.Reader(c, bytes.NewReader(source.Data)), make([]byte, 32<<10))
		return source.Kind, true, err
	}
	var kind ImageSourceKind
	switch sd.FilterPipeline[0].Name {
	case filter.DCT:
		kind = ImageSourceJPEG
	case filter.JPX:
		kind = ImageSourceJP2
	default:
		return 0, false, nil
	}
	if sd.RawLength() > imageLimits(doc.XRefTable).MaxStreamBytes {
		return 0, true, ErrImageSourceLimit
	}
	if kind == ImageSourceJP2 {
		reader, err := sd.OpenRaw()
		if err != nil {
			return 0, true, err
		}
		var header [12]byte
		n, err := io.ReadFull(reader, header[:])
		closeErr := reader.Close()
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, true, errors.Join(err, closeErr)
		}
		if closeErr != nil {
			return 0, true, closeErr
		}
		switch {
		case n == 12 && bytes.Equal(header[:], []byte{0, 0, 0, 12, 'j', 'P', ' ', ' ', 13, 10, 135, 10}):
		case n >= 4 && bytes.Equal(header[:4], []byte{255, 79, 255, 81}):
			kind = ImageSourceJ2K
		default:
			return 0, true, fmt.Errorf("%w: JPX signature", ErrInvalidImageSource)
		}
	}
	_, err := sd.WriteRawTo(contextutil.Writer(c, writer))
	return kind, true, err
}
