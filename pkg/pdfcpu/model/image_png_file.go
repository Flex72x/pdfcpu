package model

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"

	"github.com/pdfcpu/pdfcpu/internal/contextutil"
	"github.com/pdfcpu/pdfcpu/pkg/filter"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// ErrPNGFileLayout identifies layouts which still require the bounded image decoder.
var ErrPNGFileLayout = errors.New("unsupported file-backed PNG layout")

type pngFileRows struct {
	file                                 *os.File
	width, height, depth, kind, channels int
	palette                              color.Palette
	transparent                          []byte
	chunks                               []io.Reader
}

// CreatePNGImageStreamDictFromFile decodes noninterlaced PNG one row at a time.
// It retains the image model's Gray/RGB/Indexed classification, alpha, precision,
// and unpredicted Flate samples. Backing files are borrowed until document writing
// completes; the caller owns directory cleanup. Interlaced images keep the existing
// bounded decoder. Neither IDAT nor the decoded full raster is held in memory.
func CreatePNGImageStreamDictFromFile(c context.Context, xref *XRefTable, input, directory string) (_ *types.StreamDict, err error) {
	if err = contextutil.Check(c); err != nil {
		return nil, err
	}
	file, err := os.Open(input)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if stat.Size() > imageStreamLimit(xref) {
		return nil, fmt.Errorf("PNG stream exceeds resource limit")
	}
	config, err := png.DecodeConfig(contextutil.Reader(c, file))
	if err != nil {
		return nil, err
	}
	if err = validateImageResourceLimits(xref, config); err != nil {
		return nil, err
	}
	rows, err := inspectPNGFileRows(c, file, stat.Size())
	if err != nil {
		return nil, err
	}
	gray, alpha := true, false
	if err = rows.each(c, func(row image.Image) error {
		if _, indexed := row.(*image.Paletted); !indexed && !checkIfGray(row) {
			gray = false
		}
		b := row.Bounds()
		for x := range b.Dx() {
			_, _, _, a := row.At(x, 0).RGBA()
			alpha = alpha || a != 65535
		}
		return nil
	}); err != nil {
		return nil, err
	}
	// Each pass opens a fresh zlib reader over the same immutable sections.
	colorFile, err := os.CreateTemp(directory, "png-samples-*.flate")
	if err != nil {
		return nil, err
	}
	colorWriter := zlib.NewWriter(contextutil.Writer(c, colorFile))
	var maskFile *os.File
	var maskWriter *zlib.Writer
	if alpha {
		maskFile, err = os.CreateTemp(directory, "png-alpha-*.flate")
		if err != nil {
			return nil, errors.Join(err, colorWriter.Close(), colorFile.Close(), os.Remove(colorFile.Name()))
		}
		maskWriter = zlib.NewWriter(contextutil.Writer(c, maskFile))
	}
	defer func() {
		if err != nil {
			_ = os.Remove(colorFile.Name())
			if maskFile != nil {
				_ = os.Remove(maskFile.Name())
			}
		}
	}()
	var bpc int
	var cs types.Object
	err = rows.each(c, func(row image.Image) error {
		var data, mask []byte
		var rowErr error
		if indexed, ok := row.(*image.Paletted); ok {
			data, mask, rowErr = writePalettedImageBuf(xref, indexed)
			bpc, cs = 8, indexedColorSpace(indexed.Palette)
		} else {
			var a image.Image
			if gray {
				row, a = normalizeGrayImage(row)
			}
			var name string
			data, mask, bpc, name, rowErr = createImageBuf(xref, row, a, "png")
			cs = types.Name(name)
		}
		if rowErr != nil {
			return rowErr
		}
		if _, rowErr = colorWriter.Write(data); rowErr != nil {
			return rowErr
		}
		if maskWriter != nil {
			if mask == nil {
				mask = bytes.Repeat([]byte{255}, rows.width*bpc/8)
			}
			_, rowErr = maskWriter.Write(mask)
		}
		return rowErr
	})
	err = errors.Join(err, colorWriter.Close(), colorFile.Close())
	if maskFile != nil {
		err = errors.Join(err, maskWriter.Close(), maskFile.Close())
	}
	if err != nil {
		return nil, err
	}
	sd, err := pngFileSampleStream(colorFile.Name(), rows.width, rows.height, bpc, cs)
	if err != nil {
		return nil, err
	}
	if rows.width < 1000 || rows.height < 1000 {
		sd.Insert("Interpolate", types.Boolean(true))
	}
	if maskFile != nil {
		mask, e := pngFileSampleStream(maskFile.Name(), rows.width, rows.height, bpc, types.Name(DeviceGrayCS))
		if e != nil {
			return nil, e
		}
		ref, e := xref.IndRefForNewObject(*mask)
		if e != nil {
			return nil, e
		}
		sd.Insert("SMask", *ref)
	}
	return sd, nil
}

func pngFileSampleStream(path string, width, height, bpc int, cs types.Object) (*types.StreamDict, error) {
	source, err := types.NewFileStreamSource(path)
	if err != nil {
		return nil, err
	}
	sd := &types.StreamDict{Dict: types.Dict{"Type": types.Name("XObject"), "Subtype": types.Name("Image"), "Width": types.Integer(width), "Height": types.Integer(height), "BitsPerComponent": types.Integer(bpc), "ColorSpace": cs, "Filter": types.Name(filter.Flate)}, FilterPipeline: []types.PDFFilter{{Name: filter.Flate}}}
	if err = sd.SetRawSource(source); err != nil {
		return nil, err
	}
	return sd, nil
}

func inspectPNGFileRows(c context.Context, file *os.File, size int64) (*pngFileRows, error) {
	p := &pngFileRows{file: file}
	var signature [8]byte
	if _, err := file.ReadAt(signature[:], 0); err != nil {
		return nil, err
	}
	if string(signature[:]) != "\x89PNG\r\n\x1a\n" {
		return nil, fmt.Errorf("invalid PNG signature")
	}
	buffer := make([]byte, 32<<10)
	var palette, transparency []byte
	ended, sawData, dataEnded := false, false, false
	for offset := int64(8); offset+12 <= size; {
		if err := contextutil.Check(c); err != nil {
			return nil, err
		}
		var header [8]byte
		if _, err := file.ReadAt(header[:], offset); err != nil {
			return nil, err
		}
		length := int64(binary.BigEndian.Uint32(header[:4]))
		name := string(header[4:])
		if length > size-offset-12 {
			return nil, io.ErrUnexpectedEOF
		}
		crc := crc32.NewIEEE()
		_, _ = crc.Write(header[4:])
		section := io.NewSectionReader(file, offset+8, length)
		if _, err := io.CopyBuffer(crc, contextutil.Reader(c, section), buffer); err != nil {
			return nil, err
		}
		var check [4]byte
		if _, err := file.ReadAt(check[:], offset+8+length); err != nil {
			return nil, err
		}
		if crc.Sum32() != binary.BigEndian.Uint32(check[:]) {
			return nil, fmt.Errorf("PNG checksum mismatch")
		}
		switch name {
		case "IHDR":
			if offset != 8 || length != 13 {
				return nil, fmt.Errorf("invalid PNG header")
			}
			var data [13]byte
			if _, err := file.ReadAt(data[:], offset+8); err != nil {
				return nil, err
			}
			p.width, p.height = int(binary.BigEndian.Uint32(data[:4])), int(binary.BigEndian.Uint32(data[4:8]))
			p.depth, p.kind = int(data[8]), int(data[9])
			if data[10] != 0 || data[11] != 0 || data[12] != 0 {
				return nil, ErrPNGFileLayout
			}
			switch p.kind {
			case 0:
				p.channels = 1
			case 2:
				p.channels = 3
			case 3:
				p.channels = 1
			case 4:
				p.channels = 2
			case 6:
				p.channels = 4
			default:
				return nil, ErrPNGFileLayout
			}
			if p.depth != 8 && p.depth != 16 && !((p.kind == 0 || p.kind == 3) && (p.depth == 1 || p.depth == 2 || p.depth == 4)) {
				return nil, ErrPNGFileLayout
			}
			if p.kind == 3 && p.depth == 16 {
				return nil, ErrPNGFileLayout
			}
		case "PLTE", "tRNS":
			if sawData || length > 768 {
				return nil, fmt.Errorf("invalid PNG palette/transparency")
			}
			data := make([]byte, length)
			if _, err := file.ReadAt(data, offset+8); err != nil {
				return nil, err
			}
			if name == "PLTE" {
				palette = data
			} else {
				transparency = data
			}
		case "IDAT":
			if p.width <= 0 || p.height <= 0 || dataEnded {
				return nil, fmt.Errorf("invalid PNG data order")
			}
			sawData = true
			p.chunks = append(p.chunks, io.NewSectionReader(file, offset+8, length))
		case "IEND":
			if !sawData || length != 0 {
				return nil, fmt.Errorf("invalid PNG end")
			}
			ended = true
		default:
			if header[4]&32 == 0 {
				return nil, fmt.Errorf("unknown critical PNG chunk")
			}
		}
		if sawData && name != "IDAT" {
			dataEnded = true
		}
		offset += length + 12
		if ended {
			if offset != size {
				return nil, fmt.Errorf("trailing PNG data")
			}
			break
		}
	}
	if !ended {
		return nil, io.ErrUnexpectedEOF
	}
	p.transparent = transparency
	if p.kind == 3 {
		if len(palette) == 0 || len(palette)%3 != 0 || len(transparency) > len(palette)/3 {
			return nil, fmt.Errorf("invalid PNG palette")
		}
		p.palette = make(color.Palette, len(palette)/3)
		for i := range p.palette {
			a := uint8(255)
			if i < len(transparency) {
				a = transparency[i]
			}
			p.palette[i] = color.NRGBA{palette[i*3], palette[i*3+1], palette[i*3+2], a}
		}
	} else if len(transparency) > 0 && !((p.kind == 0 && len(transparency) == 2) || (p.kind == 2 && len(transparency) == 6)) {
		return nil, fmt.Errorf("invalid PNG transparency")
	}
	return p, nil
}

func (p *pngFileRows) each(c context.Context, visit func(image.Image) error) (err error) {
	readers := make([]io.Reader, len(p.chunks))
	for i, section := range p.chunks {
		original := section.(*io.SectionReader)
		_, offset, length := original.Outer()
		readers[i] = io.NewSectionReader(p.file, offset, length)
	}
	decoder, err := zlib.NewReader(contextutil.Reader(c, io.MultiReader(readers...)))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, decoder.Close()) }()
	rowBytes := (p.width*p.channels*p.depth + 7) / 8
	row, previous := make([]byte, rowBytes+1), make([]byte, rowBytes)
	stride := max(1, (p.channels*p.depth+7)/8)
	for range p.height {
		if err = contextutil.Check(c); err != nil {
			return err
		}
		if _, err = io.ReadFull(decoder, row); err != nil {
			return err
		}
		if err = unfilterPNGFileRow(row[0], row[1:], previous, stride); err != nil {
			return err
		}
		im, e := p.imageRow(row[1:])
		if e != nil {
			return e
		}
		if err = visit(im); err != nil {
			return err
		}
		copy(previous, row[1:])
	}
	var extra [1]byte
	if n, e := decoder.Read(extra[:]); n != 0 || e != io.EOF {
		if e == nil {
			e = fmt.Errorf("extra PNG sample data")
		}
		return e
	}
	return nil
}

// Filter reconstruction follows the PNG algorithms used by Go's image/png
// (Go Authors, BSD license). Only current/previous rows are retained.
func unfilterPNGFileRow(kind byte, row, previous []byte, stride int) error {
	for i := range row {
		var a, b, d byte
		if i >= stride {
			a = row[i-stride]
			d = previous[i-stride]
		}
		b = previous[i]
		switch kind {
		case 0:
		case 1:
			row[i] += a
		case 2:
			row[i] += b
		case 3:
			row[i] += byte((int(a) + int(b)) / 2)
		case 4:
			value := int(a) + int(b) - int(d)
			da, db, dd := absPNG(value-int(a)), absPNG(value-int(b)), absPNG(value-int(d))
			if da <= db && da <= dd {
				row[i] += a
			} else if db <= dd {
				row[i] += b
			} else {
				row[i] += d
			}
		default:
			return fmt.Errorf("invalid PNG filter")
		}
	}
	return nil
}
func absPNG(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func (p *pngFileRows) imageRow(row []byte) (image.Image, error) {
	bounds := image.Rect(0, 0, p.width, 1)
	if p.kind == 3 {
		out := image.NewPaletted(bounds, p.palette)
		for x := range p.width {
			index := int(row[x*p.depth/8]>>uint(8-p.depth-x*p.depth%8)) & ((1 << p.depth) - 1)
			if index >= len(p.palette) {
				return nil, fmt.Errorf("invalid PNG palette index")
			}
			out.Pix[x] = uint8(index)
		}
		return out, nil
	}
	if p.kind == 0 && len(p.transparent) == 0 {
		if p.depth == 16 {
			return &image.Gray16{Pix: row, Stride: 2 * p.width, Rect: bounds}, nil
		}
		out := image.NewGray(bounds)
		for x := range p.width {
			v := int(row[x*p.depth/8]>>uint(8-p.depth-x*p.depth%8)) & ((1 << p.depth) - 1)
			out.Pix[x] = uint8(v * 255 / ((1 << p.depth) - 1))
		}
		return out, nil
	}
	read := func(x, ch int) uint16 {
		sample := x*p.channels + ch
		if p.depth == 16 {
			return binary.BigEndian.Uint16(row[sample*2:])
		}
		if p.depth == 8 {
			return uint16(row[sample]) * 257
		}
		v := uint16(row[sample*p.depth/8]>>uint(8-p.depth-sample*p.depth%8)) & ((1 << p.depth) - 1)
		return uint16(uint32(v) * 65535 / ((1 << p.depth) - 1))
	}
	sample := func(x int) (r, g, b, a uint16) {
		r = read(x, 0)
		g, b, a = r, r, 65535
		if p.kind == 2 || p.kind == 6 {
			g, b = read(x, 1), read(x, 2)
		}
		if p.kind == 4 || p.kind == 6 {
			a = read(x, p.channels-1)
		}
		if len(p.transparent) > 0 {
			factor := uint16(1)
			if p.depth < 16 {
				factor = 65535 / ((1 << p.depth) - 1)
			}
			t := uint32(binary.BigEndian.Uint16(p.transparent[:2])) * uint32(factor)
			match := uint32(r) == t
			if p.kind == 2 {
				match = match && uint32(g) == uint32(binary.BigEndian.Uint16(p.transparent[2:4]))*uint32(factor) && uint32(b) == uint32(binary.BigEndian.Uint16(p.transparent[4:6]))*uint32(factor)
			}
			if match {
				a = 0
			}
		}
		return
	}
	if p.depth == 16 {
		out := image.NewNRGBA64(bounds)
		for x := range p.width {
			r, g, b, a := sample(x)
			out.SetNRGBA64(x, 0, color.NRGBA64{r, g, b, a})
		}
		return out, nil
	}
	out := image.NewNRGBA(bounds)
	for x := range p.width {
		r, g, b, a := sample(x)
		out.SetNRGBA(x, 0, color.NRGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)})
	}
	return out, nil
}
