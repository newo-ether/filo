// Package desktop implements a disposable connection to an original desktop owner.
package desktop

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"

	"github.com/newo-ether/filo/internal/nativejson"
)

const MaxFrameBytes = 256 * 1024 * 1024
const MaxOutputBytes = 64 * 1024 * 1024

// ReadFrame never buffers the native wire frame. Its retained projection has an
// independent limit. On error the owning follower must close only its connection.
func ReadFrame(input io.Reader) (any, error) {
	var header [4]byte
	if _, err := io.ReadFull(input, header[:]); err != nil {
		return nil, err
	}
	size := binary.LittleEndian.Uint32(header[:])
	if size == 0 || size > MaxFrameBytes {
		return nil, errors.New("invalid IPC frame size")
	}
	limited := &io.LimitedReader{R: input, N: int64(size)}
	value, _, err := nativejson.Read(limited, false)
	if err != nil {
		return nil, err
	}
	if limited.N != 0 {
		return nil, io.ErrUnexpectedEOF
	}
	return value, nil
}

func WriteFrame(output io.Writer, value any) error {
	body, err := frameBody(value)
	if err != nil {
		return err
	}
	return writeFrameBody(output, body)
}

func frameBody(value any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(body) > MaxOutputBytes {
		return nil, errors.New("IPC output limit")
	}
	return body, nil
}

func writeFrameBody(output io.Writer, body []byte) error {
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], uint32(len(body)))
	if err := writeAll(output, header[:]); err != nil {
		return err
	}
	return writeAll(output, body)
}

func writeAll(output io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := output.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
