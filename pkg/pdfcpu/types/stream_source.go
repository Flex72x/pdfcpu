/*
Copyright 2026 The pdfcpu Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may obtain a copy at https://www.apache.org/licenses/LICENSE-2.0
*/

package types

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"

	"github.com/pdfcpu/pdfcpu/internal/contextutil"
	"github.com/pdfcpu/pdfcpu/pkg/filter"
)

var ErrEncodedStreamSource = errors.New("encoded stream source failure")

type encodedSourceReader struct {
	io.ReadCloser
	remaining int64
	failure   error
}

func (reader *encodedSourceReader) Read(data []byte) (int, error) {
	if reader.failure != nil {
		return 0, reader.failure
	}
	count, err := reader.ReadCloser.Read(data)
	reader.remaining -= int64(count)
	if reader.remaining < 0 || err == io.EOF && reader.remaining > 0 {
		err = errors.Join(fmt.Errorf("%w: length mismatch", ErrEncodedStreamSource), err)
	} else if err != nil && err != io.EOF {
		err = fmt.Errorf("%w: read: %w", ErrEncodedStreamSource, err)
	}
	if errors.Is(err, ErrEncodedStreamSource) {
		reader.failure = err
	}
	return count, err
}

func (reader encodedSourceReader) Close() error {
	var err error
	if closeErr := reader.ReadCloser.Close(); closeErr != nil {
		err = fmt.Errorf("%w: close: %w", ErrEncodedStreamSource, closeErr)
	}
	return errors.Join(reader.failure, err)
}

// EncodedStreamSource provides immutable encoded stream bytes with a known
// length. Open returns a fresh reader owned by the consumer. The source owner
// keeps its backing alive until every document read/write operation completes;
// cloning a StreamDict borrows that same immutable source.
type EncodedStreamSource interface {
	Size() int64
	Open() (io.ReadCloser, error)
}

// fileStreamSource is the section [offset, offset+size) of an immutable
// regular file of fileSize bytes.
type fileStreamSource struct {
	path     string
	offset   int64
	size     int64
	fileSize int64
}

// NewFileStreamSource borrows an immutable regular file, opening it only while
// a consumer reads it. It never holds a file descriptor between operations.
func NewFileStreamSource(path string) (EncodedStreamSource, error) {
	sections, err := OpenFileSections(path)
	if err != nil {
		return nil, err
	}
	return sections.Section(0, sections.size)
}

// FileSections borrows sections of one immutable regular file, so many encoded
// streams share a single backing file. Like NewFileStreamSource, a section
// opens the file only while a consumer reads it.
type FileSections struct {
	path string
	size int64
}

// OpenFileSections inspects the file once for all of its sections.
func OpenFileSections(path string) (FileSections, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return FileSections{}, err
	}
	if !stat.Mode().IsRegular() {
		return FileSections{}, fmt.Errorf("encoded stream source is not a regular file")
	}
	return FileSections{path: path, size: stat.Size()}, nil
}

// Section returns the source of size bytes at offset.
func (f FileSections) Section(offset, size int64) (EncodedStreamSource, error) {
	if offset < 0 || size < 0 || offset > f.size-size {
		return nil, fmt.Errorf("encoded stream section outside its file")
	}
	return fileStreamSource{path: f.path, offset: offset, size: size, fileSize: f.size}, nil
}

func (s fileStreamSource) Size() int64 { return s.size }

func (s fileStreamSource) Open() (io.ReadCloser, error) {
	file, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	stat, err := file.Stat()
	if err != nil || stat.Size() != s.fileSize {
		if err == nil {
			err = fmt.Errorf("encoded stream source length changed")
		}
		return nil, errors.Join(err, file.Close())
	}
	if s.offset == 0 && s.size == s.fileSize {
		return file, nil
	}
	return sectionReadCloser{SectionReader: io.NewSectionReader(file, s.offset, s.size), Closer: file}, nil
}

type sectionReadCloser struct {
	*io.SectionReader
	io.Closer
}

// SetRawSource replaces the encoded representation without retaining a byte
// buffer or a stale decoded cache. The caller owns the source's lifetime.
func (sd *StreamDict) SetRawSource(source EncodedStreamSource) error {
	if source == nil || source.Size() < 0 {
		return fmt.Errorf("invalid encoded stream source")
	}
	length := source.Size()
	sd.Raw, sd.Content, sd.RawSource = nil, nil, source
	sd.StreamLength = &length
	sd.Update("Length", Integer(length))
	return nil
}

func (sd StreamDict) RawLength() int64 {
	if sd.RawSource != nil {
		return sd.RawSource.Size()
	}
	return int64(len(sd.Raw))
}

// OpenRaw borrows existing Raw bytes or opens the immutable source. The caller
// closes the returned reader, including on decode/write failure.
func (sd StreamDict) OpenRaw() (io.ReadCloser, error) {
	if sd.RawSource != nil {
		if sd.Raw != nil {
			return nil, fmt.Errorf("stream has both Raw and RawSource")
		}
		reader, err := sd.RawSource.Open()
		if err != nil {
			if reader != nil {
				err = errors.Join(err, reader.Close())
			}
			return nil, fmt.Errorf("%w: open: %w", ErrEncodedStreamSource, err)
		}
		if reader == nil {
			return nil, fmt.Errorf("%w: open returned no reader", ErrEncodedStreamSource)
		}
		return &encodedSourceReader{ReadCloser: reader, remaining: sd.RawSource.Size()}, nil
	}
	return io.NopCloser(bytes.NewReader(sd.Raw)), nil
}

// RawBytes is the explicit bounded materialization boundary for consumers
// whose codec/encryption still requires a byte slice.
func (sd StreamDict) RawBytes(limit int64) (_ []byte, err error) {
	length := sd.RawLength()
	if length < 0 || limit <= 0 || length > limit {
		return nil, filter.ErrDecodeLimitExceeded
	}
	if sd.RawSource == nil {
		return sd.Raw, nil
	}
	reader, err := sd.OpenRaw()
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, reader.Close()) }()
	data, err := io.ReadAll(io.LimitReader(reader, length+1))
	if err == nil && int64(len(data)) != length {
		err = fmt.Errorf("%w: length mismatch", ErrEncodedStreamSource)
	}
	return data, err
}

// WriteRawTo copies exactly the known encoded length, checks EOF, and reports
// reader close errors. Neither the original nor replacement stream is buffered.
func (sd StreamDict) WriteRawTo(writer io.Writer) (_ int64, err error) {
	if sd.RawSource == nil {
		n, err := writer.Write(sd.Raw)
		if err == nil && n != len(sd.Raw) {
			err = io.ErrShortWrite
		}
		return int64(n), err
	}
	length := sd.RawLength()
	if length < 0 {
		return 0, fmt.Errorf("invalid encoded stream source length")
	}
	reader, err := sd.OpenRaw()
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, reader.Close()) }()
	return copyEncodedStream(reader, writer, length)
}

func copyEncodedStream(reader io.Reader, writer io.Writer, length int64) (int64, error) {
	n, err := io.CopyN(writer, reader, length)
	if err != nil {
		return n, err
	}
	var extra [1]byte
	if count, readErr := reader.Read(extra[:]); count != 0 || readErr != io.EOF {
		if readErr == nil {
			readErr = fmt.Errorf("%w: length mismatch", ErrEncodedStreamSource)
		}
		return n, readErr
	}
	return n, nil
}

func (sd *StreamDict) WriteDecodedTo(c context.Context, destination io.Writer, maxDecodeBytes int64) (_ int64, err error) {
	if err = contextutil.Check(c); err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, contextutil.Check(c)) }()
	destination = contextutil.Writer(c, destination)
	if maxDecodeBytes <= 0 {
		return 0, filter.ErrDecodeLimitExceeded
	}
	raw, err := sd.OpenRaw()
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, raw.Close()) }()
	var source io.Reader = contextutil.Reader(c, raw)
	filters := sd.nonCryptFilters()
	if len(filters) == 0 {
		if sd.RawLength() > maxDecodeBytes {
			return 0, filter.ErrDecodeLimitExceeded
		}
		return copyEncodedStream(source, destination, sd.RawLength())
	}
	for index, current := range filters {
		if err := contextutil.Check(c); err != nil {
			return 0, err
		}
		if preserveEncodedImageFilter(current.Name) || current.Name == filter.DCT && sd.CSComponents != 4 {
			if preserveEncodedImageFilter(current.Name) && index != len(filters)-1 {
				return 0, fmt.Errorf("stream filter[%d] %q: decode: %w", index, current.Name, filter.ErrUnsupportedFilter)
			}
			if maxDecodeBytes < math.MaxInt64 {
				source = io.LimitReader(source, maxDecodeBytes+1)
			}
			count, err := io.CopyBuffer(destination, source, make([]byte, 32<<10))
			if count > maxDecodeBytes {
				err = errors.Join(err, filter.ErrDecodeLimitExceeded)
			}
			return count, err
		}
		parms := parmsForFilter(current.DecodeParms)
		if err := fixParms(current, parms, sd); err != nil {
			return 0, fmt.Errorf("stream filter[%d] %q: prepare parameters: %w", index, current.Name, err)
		}
		if index == len(filters)-1 {
			return filter.DecodeTo(current.Name, parms, maxDecodeBytes, contextutil.Reader(c, source), destination)
		}
		var intermediate bytes.Buffer
		if _, err := filter.DecodeTo(current.Name, parms, maxDecodeBytes, contextutil.Reader(c, source), contextutil.Writer(c, &intermediate)); err != nil {
			return 0, fmt.Errorf("stream filter[%d] %q: decode: %w", index, current.Name, err)
		}
		source = bytes.NewReader(intermediate.Bytes())
	}
	return 0, nil
}
