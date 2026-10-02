package contextutil

import (
	"context"
	"io"
)

type contextWriter struct {
	ctx    context.Context
	writer io.Writer
}

// Writer checks cancellation at every buffered flush/stream-copy chunk.
// The caller owns and closes the underlying writer.
func Writer(ctx context.Context, writer io.Writer) io.Writer {
	return contextWriter{ctx: ctx, writer: writer}
}

func (w contextWriter) Write(data []byte) (int, error) {
	if err := Check(w.ctx); err != nil {
		return 0, err
	}
	return w.writer.Write(data)
}
