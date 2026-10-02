package contextutil

import (
	"context"
	"io"
)

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

// Reader prevents io.Copy from bypassing cancellation through WriterTo.
func Reader(ctx context.Context, reader io.Reader) io.Reader {
	return contextReader{ctx: ctx, reader: reader}
}

func (r contextReader) Read(data []byte) (int, error) {
	if err := Check(r.ctx); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}
