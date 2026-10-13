package archive

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Conflict decides what happens when a file already exists at the destination.
type Conflict string

const (
	ConflictSkip      Conflict = "skip"
	ConflictOverwrite Conflict = "overwrite"
	ConflictRename    Conflict = "rename"
)

// ExtractOptions control how an archive is unpacked.
type ExtractOptions struct {
	Passphrase string
	Keyfile    []byte
	Conflict   Conflict
	KeepModes  bool // restore permissions and modification times
	Progress   func(Progress)
}

// ExtractResult summarises a finished extract run.
type ExtractResult struct {
	Files    int
	Dirs     int
	Bytes    int64
	Skipped  int
	Warnings []string
}

// Item describes one entry inside an archive.
type Item struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	IsDir bool   `json:"isDir"`
}

type stream struct {
	tr    *tar.Reader
	close func()
	total int64
}

func openStream(r io.Reader, pass string, keyfile []byte) (*stream, error) {
	op, err := newOpener(newBufReader(r), pass, keyfile)
	if err != nil {
		return nil, err
	}
	zr, err := zstd.NewReader(op)
	if err != nil {
		return nil, err
	}
	return &stream{tr: tar.NewReader(zr), close: zr.Close}, nil
}

// nextHeader returns the next real entry and records the size hint if seen.
func (s *stream) nextHeader() (*tar.Header, error) {
	for {
		h, err := s.tr.Next()
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			if v, ok := h.PAXRecords[totalKey]; ok {
				s.total, _ = strconv.ParseInt(v, 10, 64)
			}
			continue
		}
		return h, nil
	}
}

// List decrypts the archive and returns its entries without writing anything.
func List(r io.Reader, pass string, keyfile []byte) ([]Item, error) {
	s, err := openStream(r, pass, keyfile)
	if err != nil {
		return nil, err
	}
	defer s.close()
	var items []Item
	for {
		h, err := s.nextHeader()
		if err == io.EOF {
			return items, nil
		}
		if err != nil {
			return nil, err
		}
		items = append(items, Item{
			Path:  strings.TrimSuffix(h.Name, "/"),
			Size:  h.Size,
			IsDir: h.Typeflag == tar.TypeDir,
		})
	}
}

// Extract decrypts the archive into dst, creating it when needed.
func Extract(r io.Reader, dst string, opt ExtractOptions) (*ExtractResult, error) {
	if opt.Conflict == "" {
		opt.Conflict = ConflictRename
	}
	s, err := openStream(r, opt.Passphrase, opt.Keyfile)
	if err != nil {
		return nil, err
	}
	defer s.close()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return nil, err
	}

	res := &ExtractResult{}
	prog := Progress{}
	type dirMeta struct {
		path string
		mode os.FileMode
		mod  time.Time
	}
	var dirs []dirMeta

	for {
		h, err := s.nextHeader()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		prog.Total = s.total
		target, err := safeJoin(dst, h.Name)
		if err != nil {
			return nil, err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return nil, err
			}
			res.Dirs++
			dirs = append(dirs, dirMeta{target, h.FileInfo().Mode().Perm(), h.ModTime})
		case tar.TypeReg:
			prog.Current = h.Name
			if opt.Progress != nil {
				opt.Progress(prog)
			}
			written, skipped, err := writeOut(s.tr, target, h, opt, &prog)
			if err != nil {
				return nil, err
			}
			if skipped {
				res.Skipped++
				continue
			}
			res.Files++
			res.Bytes += written
			prog.Files++
		default:
			res.Warnings = append(res.Warnings, fmt.Sprintf("ignored unsupported entry: %s", h.Name))
		}
	}

	if opt.KeepModes {
		// Deepest first, so setting a read-only mode on a parent comes last.
		sort.Slice(dirs, func(i, j int) bool { return len(dirs[i].path) > len(dirs[j].path) })
		for _, d := range dirs {
			os.Chtimes(d.path, d.mod, d.mod)
			os.Chmod(d.path, d.mode|0o700)
		}
	}
	if opt.Progress != nil {
		prog.Current = ""
		opt.Progress(prog)
	}
	return res, nil
}

// safeJoin maps an archive name onto dst and refuses anything that would land outside it.
func safeJoin(dst, name string) (string, error) {
	clean := path.Clean(strings.ReplaceAll(name, "\\", "/"))
	local := filepath.FromSlash(clean)
	if clean == "." || !filepath.IsLocal(local) {
		return "", fmt.Errorf("archive contains an unsafe path: %q", name)
	}
	return filepath.Join(dst, local), nil
}

func writeOut(src io.Reader, target string, h *tar.Header, opt ExtractOptions, prog *Progress) (int64, bool, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, false, err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if opt.Conflict == ConflictOverwrite {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	var f *os.File
	var err error
	for n := 0; ; n++ {
		p := target
		if n > 0 {
			p = numbered(target, n)
		}
		f, err = os.OpenFile(p, flags, 0o600)
		if err == nil {
			target = p
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return 0, false, err
		}
		if opt.Conflict == ConflictSkip {
			if _, derr := io.Copy(io.Discard, src); derr != nil {
				return 0, false, derr
			}
			return 0, true, nil
		}
	}

	pr := &countingReader{r: src, onRead: func(n int) {
		prog.Bytes += int64(n)
		if opt.Progress != nil {
			opt.Progress(*prog)
		}
	}}
	n, err := io.Copy(f, pr)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, false, err
	}
	if opt.KeepModes {
		os.Chmod(target, h.FileInfo().Mode().Perm()|0o600)
		os.Chtimes(target, h.ModTime, h.ModTime)
	} else {
		os.Chmod(target, 0o644)
	}
	return n, false, nil
}

// numbered turns "report.txt" into "report (2).txt".
func numbered(p string, n int) string {
	ext := filepath.Ext(p)
	return fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(p, ext), n+1, ext)
}
