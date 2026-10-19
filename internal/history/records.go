package history

import (
	"context"
	"errors"
	"io"
	"iter"
)

const scanBytes = 65536

type recordSpan struct{ start, end int64 }

// records walks complete JSONL records backwards with one fixed buffer. A
// trailing unfinished append never becomes a message or a cursor boundary.
// An optional lower bound stops reads even inside a single oversized record.
func records(ctx context.Context, file io.ReaderAt, before int64, lowerBound ...int64) iter.Seq2[recordSpan, error] {
	return func(yield func(recordSpan, error) bool) {
		lower := int64(0)
		if len(lowerBound) > 0 {
			lower = min(before, max(int64(0), lowerBound[0]))
		}
		buffer := make([]byte, scanBytes)
		position, end := before, int64(-1)
		for position > lower {
			if err := ctx.Err(); err != nil {
				yield(recordSpan{}, err)
				return
			}
			start := max(lower, position-int64(len(buffer)))
			length := int(position - start)
			n, err := file.ReadAt(buffer[:length], start)
			if n != length {
				yield(recordSpan{}, errors.New("Native history changed during read"))
				return
			}
			if err != nil && err != io.EOF {
				yield(recordSpan{}, err)
				return
			}
			for i := length - 1; i >= 0; i-- {
				if buffer[i] != 10 {
					continue
				}
				at := start + int64(i)
				if end >= 0 && end > at+1 && !yield(recordSpan{at + 1, end}, nil) {
					return
				}
				end = at
			}
			position = start
		}
		if lower == 0 && end > 0 {
			yield(recordSpan{0, end}, nil)
		}
	}
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(buffer)
}
