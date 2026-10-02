package filter

import (
	"errors"
	"fmt"
	"io"
)

// DecodeTo writes full decoded samples with the same limits and predictor
// algorithms as Decode. Flate retains only zlib state and two rows; filters
// needing whole-stream codecs retain their existing bounded decoder.
func DecodeTo(name string, parms map[string]int, maxDecodeBytes int64, source io.Reader, destination io.Writer) (int64, error) {
	decoder, err := NewFilter(name, parms, maxDecodeBytes)
	if err != nil {
		return 0, err
	}
	if f, ok := decoder.(flate); ok {
		return f.decodeTo(source, destination)
	}
	decoded, err := decoder.Decode(source)
	if err != nil {
		return 0, err
	}
	return io.CopyBuffer(destination, decoded, make([]byte, 32<<10))
}

func (f flate) decodeTo(source io.Reader, destination io.Writer) (total int64, err error) {
	rc, err := acquireZlibReader(source)
	if err != nil {
		return 0, err
	}
	tracking := zlibErrorTrackingReader{r: rc}
	// The reader pool is reused only after successful decoding.
	defer func() { releaseZlibReader(rc, err == nil && tracking.err == nil) }()
	predictor, found := f.parms["Predictor"]
	if !found || predictor == PredictorNo {
		total, err = io.CopyBuffer(destination, io.LimitReader(&tracking, f.maxDecodeBytes+1), make([]byte, 32<<10))
		if total > f.maxDecodeBytes {
			err = ErrDecodeLimitExceeded
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			logUnexpectedEOFFlateDecode()
			err = nil
		}
		return total, err
	}
	if err = validatePredictor(predictor); err != nil {
		return 0, err
	}
	colors, bpc, columns, err := f.parameters()
	if err != nil {
		return 0, err
	}
	_, rowLen, stride, err := predictorRowParams(predictor, colors, bpc, columns)
	if err != nil {
		return 0, err
	}
	if int64(rowLen) > f.maxDecodeBytes {
		return 0, ErrDecodeLimitExceeded
	}
	current, previous := make([]byte, rowLen), make([]byte, rowLen)
	for {
		n, e := io.ReadFull(&tracking, current)
		if e != nil {
			if n == 0 && (errors.Is(e, io.EOF) || errors.Is(e, io.ErrUnexpectedEOF)) {
				return total, nil
			}
			return total, e
		}
		if n != rowLen {
			return total, fmt.Errorf("flate decode: read error, expected %d bytes, got: %d", rowLen, n)
		}
		decoded, e := processRow(previous, current, predictor, colors, stride)
		if e != nil {
			return total, e
		}
		if int64(len(decoded)) > f.maxDecodeBytes-total {
			return total, ErrDecodeLimitExceeded
		}
		written, e := destination.Write(decoded)
		total += int64(written)
		if e != nil {
			return total, e
		}
		if written != len(decoded) {
			return total, io.ErrShortWrite
		}
		previous, current = current, previous
	}
}
