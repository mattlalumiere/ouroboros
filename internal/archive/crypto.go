// Package archive implements the encrypted container used by ouroboros.
//
// Layout on disk:
//
//	header | chunk | chunk | ... | final chunk
//
// The plaintext stream is a tar file compressed with zstd. It is cut into
// fixed size chunks and every chunk is sealed with AES-256-GCM. The header is
// authenticated as additional data for every chunk, the nonce carries a chunk
// counter, and the last chunk is flagged, so reordering, truncation and
// tampering are all detected.
package archive

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
)

var magic = [8]byte{'O', 'U', 'R', 'O', 'B', 'O', 'R', '1'}

const (
	flagKeyfile = 1 << 0

	headerSize   = 8 + 1 + 1 + 4 + 1 + 16 + 4 + 1
	saltSize     = 16
	chunkLog2    = 16 // 64 KiB of plaintext per chunk
	maxMemoryKiB = 1 << 20
	maxChunkLog2 = 24

	defaultTime    = 3
	defaultMemory  = 256 * 1024 // KiB
	defaultThreads = 4
)

// ErrWrongKey is returned when the first chunk fails authentication. It means
// either the passphrase or keyfile is wrong, or the data is damaged.
var ErrWrongKey = errors.New("wrong passphrase or keyfile, or the archive is damaged")

// ErrNotArchive is returned when the data does not start with the expected magic.
var ErrNotArchive = errors.New("no ouroboros archive found")

type header struct {
	flags     byte
	time      byte
	memoryKiB uint32
	threads   byte
	salt      [saltSize]byte
	noncePre  [4]byte
	chunkLog2 byte
}

func (h *header) marshal() []byte {
	b := make([]byte, 0, headerSize)
	b = append(b, magic[:]...)
	b = append(b, h.flags, h.time)
	b = binary.BigEndian.AppendUint32(b, h.memoryKiB)
	b = append(b, h.threads)
	b = append(b, h.salt[:]...)
	b = append(b, h.noncePre[:]...)
	b = append(b, h.chunkLog2)
	return b
}

func parseHeader(raw []byte) (*header, error) {
	if len(raw) != headerSize || string(raw[:8]) != string(magic[:]) {
		return nil, ErrNotArchive
	}
	h := &header{}
	h.flags = raw[8]
	h.time = raw[9]
	h.memoryKiB = binary.BigEndian.Uint32(raw[10:14])
	h.threads = raw[14]
	copy(h.salt[:], raw[15:15+saltSize])
	copy(h.noncePre[:], raw[15+saltSize:19+saltSize])
	h.chunkLog2 = raw[19+saltSize]
	if h.time == 0 || h.threads == 0 || h.memoryKiB == 0 || h.memoryKiB > maxMemoryKiB ||
		h.chunkLog2 < 10 || h.chunkLog2 > maxChunkLog2 {
		return nil, errors.New("archive header has invalid parameters")
	}
	return h, nil
}

// secret combines the passphrase with an optional keyfile. The keyfile is
// reduced to a hash and used as an HMAC key over the passphrase, so a missing
// or different keyfile gives a completely different key.
func secret(passphrase string, keyfile []byte) []byte {
	if keyfile == nil {
		return []byte(passphrase)
	}
	kh := sha256.Sum256(keyfile)
	m := hmac.New(sha256.New, kh[:])
	m.Write([]byte(passphrase))
	return m.Sum(nil)
}

func deriveKey(h *header, passphrase string, keyfile []byte) []byte {
	return argon2.IDKey(secret(passphrase, keyfile), h.salt[:], uint32(h.time), h.memoryKiB, h.threads, 32)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

func nonceFor(pre [4]byte, counter uint64) []byte {
	n := make([]byte, 12)
	copy(n, pre[:])
	binary.BigEndian.PutUint64(n[4:], counter)
	return n
}

// sealer is an io.WriteCloser that encrypts everything written to it.
type sealer struct {
	w       io.Writer
	aead    cipher.AEAD
	hdr     []byte
	pre     [4]byte
	buf     []byte
	size    int
	counter uint64
	err     error
}

func newSealer(w io.Writer, passphrase string, keyfile []byte) (*sealer, error) {
	h := &header{
		time:      defaultTime,
		memoryKiB: defaultMemory,
		threads:   defaultThreads,
		chunkLog2: chunkLog2,
	}
	if keyfile != nil {
		h.flags |= flagKeyfile
	}
	if _, err := io.ReadFull(rand.Reader, h.salt[:]); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(rand.Reader, h.noncePre[:]); err != nil {
		return nil, err
	}
	aead, err := newGCM(deriveKey(h, passphrase, keyfile))
	if err != nil {
		return nil, err
	}
	raw := h.marshal()
	if _, err := w.Write(raw); err != nil {
		return nil, err
	}
	return &sealer{w: w, aead: aead, hdr: raw, pre: h.noncePre, size: 1 << h.chunkLog2}, nil
}

func (s *sealer) Write(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	total := len(p)
	for len(p) > 0 {
		// A full buffer is only flushed once more data arrives, so Close
		// always has a last chunk to mark as final.
		if len(s.buf) == s.size {
			if err := s.flush(false); err != nil {
				return total - len(p), err
			}
		}
		n := min(s.size-len(s.buf), len(p))
		s.buf = append(s.buf, p[:n]...)
		p = p[n:]
	}
	return total, nil
}

func (s *sealer) Close() error {
	if s.err != nil {
		return s.err
	}
	return s.flush(true)
}

func (s *sealer) flush(final bool) error {
	aad := append(append([]byte{}, s.hdr...), finalByte(final))
	ct := s.aead.Seal(nil, nonceFor(s.pre, s.counter), s.buf, aad)
	s.counter++
	s.buf = s.buf[:0]
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(ct)))
	if _, err := s.w.Write(l[:]); err != nil {
		s.err = err
		return err
	}
	if _, err := s.w.Write(ct); err != nil {
		s.err = err
		return err
	}
	return nil
}

func finalByte(final bool) byte {
	if final {
		return 1
	}
	return 0
}

// opener is an io.Reader that decrypts a stream produced by sealer.
type opener struct {
	r       peekReader
	aead    cipher.AEAD
	hdr     []byte
	pre     [4]byte
	maxCT   int
	counter uint64
	out     []byte
	done    bool
}

type peekReader interface {
	io.Reader
	Peek(n int) ([]byte, error)
}

// NeedsKeyfile reads only the header and reports whether a keyfile is needed.
func NeedsKeyfile(r io.Reader) (bool, error) {
	raw := make([]byte, headerSize)
	if _, err := io.ReadFull(r, raw); err != nil {
		return false, ErrNotArchive
	}
	h, err := parseHeader(raw)
	if err != nil {
		return false, err
	}
	return h.flags&flagKeyfile != 0, nil
}

func newOpener(r peekReader, passphrase string, keyfile []byte) (*opener, error) {
	raw := make([]byte, headerSize)
	if _, err := io.ReadFull(r, raw); err != nil {
		return nil, ErrNotArchive
	}
	h, err := parseHeader(raw)
	if err != nil {
		return nil, err
	}
	if h.flags&flagKeyfile != 0 && keyfile == nil {
		return nil, errors.New("this archive needs a keyfile")
	}
	aead, err := newGCM(deriveKey(h, passphrase, keyfile))
	if err != nil {
		return nil, err
	}
	o := &opener{
		r: r, aead: aead, hdr: raw, pre: h.noncePre,
		maxCT: (1 << h.chunkLog2) + aead.Overhead(),
	}
	// Read the first chunk now so a wrong key is reported immediately.
	if err := o.next(); err != nil {
		if errors.Is(err, errAuth) {
			return nil, ErrWrongKey
		}
		return nil, err
	}
	return o, nil
}

var errAuth = errors.New("authentication failed")

func (o *opener) next() error {
	var l [4]byte
	if _, err := io.ReadFull(o.r, l[:]); err != nil {
		return fmt.Errorf("archive is truncated: %w", err)
	}
	n := int(binary.BigEndian.Uint32(l[:]))
	if n < o.aead.Overhead() || n > o.maxCT {
		return errors.New("archive is corrupted (bad chunk size)")
	}
	ct := make([]byte, n)
	if _, err := io.ReadFull(o.r, ct); err != nil {
		return fmt.Errorf("archive is truncated: %w", err)
	}
	_, perr := o.r.Peek(1)
	final := perr == io.EOF
	aad := append(append([]byte{}, o.hdr...), finalByte(final))
	pt, err := o.aead.Open(nil, nonceFor(o.pre, o.counter), ct, aad)
	if err != nil {
		return errAuth
	}
	o.counter++
	o.out = pt
	o.done = final
	return nil
}

func (o *opener) Read(p []byte) (int, error) {
	for len(o.out) == 0 {
		if o.done {
			return 0, io.EOF
		}
		if err := o.next(); err != nil {
			if errors.Is(err, errAuth) {
				return 0, errors.New("archive is corrupted or has been tampered with")
			}
			return 0, err
		}
	}
	n := copy(p, o.out)
	o.out = o.out[n:]
	return n, nil
}
