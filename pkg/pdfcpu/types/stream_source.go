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
	"github.com/pdfcpu/pdfcpu/internal/contextutil"
	"io"
	"os"

	"github.com/pdfcpu/pdfcpu/pkg/filter"
)

// EncodedStreamSource provides immutable encoded stream bytes with a known
// length. Open returns a fresh reader owned by the consumer. The source owner
// keeps its backing alive until every document read/write operation completes;
// cloning a StreamDict borrows that same immutable source.
type EncodedStreamSource interface {
	Size() int64
	Open() (io.ReadCloser, error)
}

type fileStreamSource struct {
	path string
	size int64
}

// NewFileStreamSource borrows an immutable regular file, opening it only while
// a consumer reads it. It never holds a file descriptor between operations.
func NewFileStreamSource(path string) (EncodedStreamSource, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !stat.Mode().IsRegular() {
		return nil, fmt.Errorf("encoded stream source is not a regular file")
	}
	return fileStreamSource{path: path, size: stat.Size()}, nil
}

func (s fileStreamSource) Size() int64 { return s.size }

func (s fileStreamSource) Open() (io.ReadCloser, error) {
	file, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	stat, err := file.Stat()
	if err != nil || stat.Size() != s.size {
		if err == nil {
			err = fmt.Errorf("encoded stream source length changed")
		}
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
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
		return sd.RawSource.Open()
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
		err = fmt.Errorf("encoded stream source length mismatch")
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
	n, err := io.CopyN(writer, reader, length)
	if err != nil {
		return n, err
	}
	var extra [1]byte
	if count, readErr := reader.Read(extra[:]); count != 0 || readErr != io.EOF {
		if readErr == nil {
			readErr = fmt.Errorf("encoded stream source length mismatch")
		}
		return n, readErr
	}
	return n, nil
}

// WriteDecodedTo retains native stream backing for Flate samples. Other filters
// use their existing bounded decoder. Content caches are never installed here.
func (sd *StreamDict) WriteDecodedTo(c context.Context, destination io.Writer, maxDecodeBytes int64) (_ int64, err error) {
	if err = contextutil.Check(c); err != nil {
		return 0, err
	}
	destination = contextutil.Writer(c, destination)
	if maxDecodeBytes <= 0 {
		return 0, filter.ErrDecodeLimitExceeded
	}
	if len(sd.FilterPipeline) == 0 {
		if sd.RawLength() > maxDecodeBytes {
			return 0, filter.ErrDecodeLimitExceeded
		}
		return sd.WriteRawTo(destination)
	}
	if len(sd.FilterPipeline) != 1 {
		local := *sd
		local.Content = nil
		data, err := local.DecodeLengthWithLimit(-1, maxDecodeBytes)
		if err != nil {
			return 0, err
		}
		n, err := destination.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		return int64(n), err
	}
	f := sd.FilterPipeline[0]
	parms := parmsForFilter(f.DecodeParms)
	if err = fixParms(f, parms, sd); err != nil {
		return 0, err
	}
	raw, err := sd.OpenRaw()
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, raw.Close()) }()
	return filter.DecodeTo(f.Name, parms, maxDecodeBytes, contextutil.Reader(c, raw), destination)
}
