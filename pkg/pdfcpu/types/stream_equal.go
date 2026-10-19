package types

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/pdfcpu/pdfcpu/internal/contextutil"
)

func (sd StreamDict) EqualRaw(c context.Context, other StreamDict) (equal bool, err error) {
	if err := contextutil.Check(c); err != nil {
		return false, err
	}
	defer func() {
		err = errors.Join(err, contextutil.Check(c))
		if err != nil {
			equal = false
		}
	}()
	length := sd.RawLength()
	otherLength := other.RawLength()
	if length < 0 || otherLength < 0 {
		return false, fmt.Errorf("%w: invalid encoded length", ErrEncodedStreamSource)
	}
	if length != otherLength {
		return false, nil
	}
	const chunkSize = 32 << 10
	if sd.RawSource == nil && other.RawSource == nil {
		for start := 0; start < len(sd.Raw); start += chunkSize {
			if err := contextutil.Check(c); err != nil {
				return false, err
			}
			end := start + min(chunkSize, len(sd.Raw)-start)
			if !bytes.Equal(sd.Raw[start:end], other.Raw[start:end]) {
				return false, nil
			}
		}
		return true, nil
	}
	first, err := sd.OpenRaw()
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, first.Close()) }()
	if err := contextutil.Check(c); err != nil {
		return false, err
	}
	second, err := other.OpenRaw()
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, second.Close()) }()
	left, right := contextutil.Reader(c, first), contextutil.Reader(c, second)
	firstChunk, secondChunk := make([]byte, chunkSize), make([]byte, chunkSize)
	for remaining := length; remaining > 0; {
		count := int(min(remaining, int64(chunkSize)))
		if _, err := io.ReadFull(left, firstChunk[:count]); err != nil {
			return false, err
		}
		if _, err := io.ReadFull(right, secondChunk[:count]); err != nil {
			return false, err
		}
		if !bytes.Equal(firstChunk[:count], secondChunk[:count]) {
			return false, nil
		}
		remaining -= int64(count)
	}
	var extra [1]byte
	for _, reader := range []io.Reader{left, right} {
		if count, err := reader.Read(extra[:]); count != 0 || err != io.EOF {
			if err == nil {
				err = fmt.Errorf("%w: length mismatch", ErrEncodedStreamSource)
			}
			return false, err
		}
	}
	return true, nil
}
