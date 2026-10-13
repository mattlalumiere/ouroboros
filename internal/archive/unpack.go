package archive

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

// Limits for archives that may come from someone else. The numbers are far
// above what real folders need, they only stop absurd or hostile ones.
const (
	maxEntries     = 2_000_000
	maxNameLen     = 4096
	maxZstdWindow  = 128 << 20
	errEscapesRoot = "the destination contains a link that leads outside it"
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
	count int
}

func openStream(r io.Reader, pass string, keyfile []byte) (*stream, error) {
	op, err := newOpener(newBufReader(r), pass, keyfile)
	if err != nil {
		return nil, err
	}
	zr, err := zstd.NewReader(op,
		zstd.WithDecoderMaxWindow(maxZstdWindow),
		zstd.WithDecoderConcurrency(1))
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
		if s.count++; s.count > maxEntries {
			return nil, errors.New("archive has too many entries")
		}
		if len(h.Name) > maxNameLen {
			return nil, errors.New("archive contains an unreasonably long name")
		}
		return h, nil
	}
}

// List decrypts the archive and returns its entries without writing anything.
// Names are made safe to print.
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
			Path:  Printable(strings.TrimSuffix(h.Name, "/")),
			Size:  h.Size,
			IsDir: h.Typeflag == tar.TypeDir,
		})
	}
}

type dirMeta struct {
	rel  string
	mode os.FileMode
	mod  time.Time
}

// Extract decrypts the archive into dst, creating it when needed.
//
// Every filesystem call goes through an os.Root opened on dst, so no entry
// can end up outside it, even when the destination already contains links
// that point elsewhere.
func Extract(r io.Reader, dst string, opt ExtractOptions) (*ExtractResult, error) {
	if opt.Conflict == "" {
		opt.Conflict = ConflictRename
	}
	s, err := openStream(r, opt.Passphrase, opt.Keyfile)
	if err != nil {
		return nil, err
	}
	defer s.close()
	if err := os.MkdirAll(dst, 0o777); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dst)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	res := &ExtractResult{}
	prog := Progress{}
	dirs := map[string]dirMeta{}

	// Directory modes and times are applied last, and also after a failure,
	// so a half finished run does not leave odd permissions behind.
	defer func() {
		if !opt.KeepModes {
			return
		}
		list := make([]dirMeta, 0, len(dirs))
		for _, d := range dirs {
			list = append(list, d)
		}
		sort.Slice(list, func(i, j int) bool { return len(list[i].rel) > len(list[j].rel) })
		for _, d := range list {
			root.Chtimes(d.rel, d.mod, d.mod)
			root.Chmod(d.rel, d.mode|0o700)
		}
	}()

	for {
		h, err := s.nextHeader()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		prog.Total = s.total
		rel, err := cleanRel(h.Name)
		if err != nil {
			return nil, err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(rel, 0o777); err != nil {
				return nil, rootErr(err)
			}
			res.Dirs++
			dirs[rel] = dirMeta{rel, h.FileInfo().Mode().Perm(), h.ModTime}
		case tar.TypeReg:
			prog.Current = Printable(h.Name)
			if opt.Progress != nil {
				opt.Progress(prog)
			}
			written, skipped, err := writeOut(root, s.tr, rel, h, opt, &prog)
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
			res.Warnings = append(res.Warnings, "ignored unsupported entry: "+Printable(h.Name))
		}
	}

	if opt.Progress != nil {
		prog.Current = ""
		opt.Progress(prog)
	}
	return res, nil
}

// cleanRel turns an archive name into a relative path that stays inside the
// destination, or refuses it.
func cleanRel(name string) (string, error) {
	clean := path.Clean(strings.ReplaceAll(name, "\\", "/"))
	local := filepath.FromSlash(clean)
	if clean == "." || !filepath.IsLocal(local) {
		return "", fmt.Errorf("archive contains an unsafe path: %q", Printable(name))
	}
	return local, nil
}

// safeJoin maps an archive name onto dst and refuses anything that would land outside it.
func safeJoin(dst, name string) (string, error) {
	rel, err := cleanRel(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dst, rel), nil
}

func rootErr(err error) error {
	if err != nil && strings.Contains(err.Error(), "escapes from parent") {
		return errors.New(errEscapesRoot)
	}
	return err
}

func writeOut(root *os.Root, src io.Reader, rel string, h *tar.Header, opt ExtractOptions, prog *Progress) (int64, bool, error) {
	if dir := filepath.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0o777); err != nil {
			return 0, false, rootErr(err)
		}
	}

	// Without KeepModes new files get the usual default (the umask decides),
	// with it they start private and get their real mode once written.
	perm := os.FileMode(0o666)
	if opt.KeepModes {
		perm = 0o600
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if opt.Conflict == ConflictOverwrite {
		// Overwriting is only for plain files. A link or folder in the way is
		// never followed or replaced.
		if st, err := root.Lstat(rel); err == nil && !st.Mode().IsRegular() {
			return 0, false, fmt.Errorf("refusing to overwrite %q because it is not a regular file", Printable(rel))
		}
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}

	var f *os.File
	var err error
	target := rel
	for n := 0; ; n++ {
		if n > 0 {
			target = numbered(rel, n)
		}
		f, err = root.OpenFile(target, flags, perm)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return 0, false, rootErr(err)
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
		// Do not leave a truncated file behind.
		root.Remove(target)
		return n, false, err
	}
	if opt.KeepModes {
		root.Chmod(target, h.FileInfo().Mode().Perm()|0o600)
		root.Chtimes(target, h.ModTime, h.ModTime)
	}
	return n, false, nil
}

// numbered turns "report.txt" into "report (2).txt".
func numbered(p string, n int) string {
	ext := filepath.Ext(p)
	return fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(p, ext), n+1, ext)
}
