// Package payload glues an archive onto the end of an executable and finds it again.
//
// An executable carrying a payload ends with: payload bytes, then a 16 byte
// trailer made of a magic string and the payload length. Operating systems
// ignore data after the end of an executable image, so the file still runs.
package payload

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
)

var trailerMagic = [8]byte{'O', 'U', 'R', 'O', 'P', 'A', 'Y', '1'}

const trailerSize = 16

// ErrNone means the file has no payload attached.
var ErrNone = errors.New("no payload attached")

// Write copies stub to w, then whatever fill writes, then the trailer.
func Write(w io.Writer, stub io.Reader, fill func(io.Writer) error) error {
	if _, err := io.Copy(w, stub); err != nil {
		return err
	}
	cw := &countWriter{w: w}
	if err := fill(cw); err != nil {
		return err
	}
	var t [trailerSize]byte
	copy(t[:], trailerMagic[:])
	binary.BigEndian.PutUint64(t[8:], uint64(cw.n))
	_, err := w.Write(t[:])
	return err
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// Open returns a reader over the payload of the file at path. The caller
// closes the returned file when done.
func Open(path string) (*io.SectionReader, *os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	size := st.Size()
	if size < trailerSize {
		f.Close()
		return nil, nil, ErrNone
	}
	var t [trailerSize]byte
	if _, err := f.ReadAt(t[:], size-trailerSize); err != nil {
		f.Close()
		return nil, nil, err
	}
	n := int64(binary.BigEndian.Uint64(t[8:]))
	if string(t[:8]) != string(trailerMagic[:]) || n < 0 || n > size-trailerSize {
		f.Close()
		return nil, nil, ErrNone
	}
	return io.NewSectionReader(f, size-trailerSize-n, n), f, nil
}
