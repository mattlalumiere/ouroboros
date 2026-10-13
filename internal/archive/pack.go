package archive

import (
	"archive/tar"
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/klauspost/compress/zstd"
)

// Level selects the compression effort.
type Level string

const (
	LevelFast    Level = "fast"
	LevelDefault Level = "default"
	LevelBetter  Level = "better"
	LevelBest    Level = "best"
)

func (l Level) zstd() zstd.EncoderLevel {
	switch l {
	case LevelFast:
		return zstd.SpeedFastest
	case LevelBetter:
		return zstd.SpeedBetterCompression
	case LevelBest:
		return zstd.SpeedBestCompression
	}
	return zstd.SpeedDefault
}

// Progress is reported while packing or extracting.
type Progress struct {
	Files   int
	Bytes   int64
	Total   int64 // total payload bytes, 0 when unknown
	Current string
}

// PackOptions control how an archive is written.
type PackOptions struct {
	Passphrase string
	Keyfile    []byte // optional extra secret
	Level      Level
	Progress   func(Progress)
	// Exclude lists files that must never be packed, such as the output being
	// written. They are matched with os.SameFile, so links and case tricks
	// cannot hide them.
	Exclude []os.FileInfo
}

// Archived is something that was stored in the archive, as it was on disk.
type Archived struct {
	Path    string
	IsDir   bool
	Size    int64
	ModTime time.Time
}

// PackResult summarises a finished pack run.
type PackResult struct {
	Files    int
	Dirs     int
	Bytes    int64
	Warnings []string
	Entries  []Archived // everything that was stored, in archive order
}

const totalKey = "OUROBOROS.total"

type entry struct {
	abs  string
	name string // slash separated name inside the archive
	info fs.FileInfo
}

// Pack writes the given files and directories to w as an encrypted archive.
// Each path is stored under its own base name, with directory contents below it.
func Pack(w io.Writer, paths []string, opt PackOptions) (*PackResult, error) {
	if opt.Passphrase == "" {
		return nil, fmt.Errorf("a passphrase is required")
	}
	res := &PackResult{}
	entries, total, err := collect(paths, res, opt.Exclude)
	if err != nil {
		return nil, err
	}

	seal, err := newSealer(w, opt.Passphrase, opt.Keyfile)
	if err != nil {
		return nil, err
	}
	zw, err := zstd.NewWriter(seal, zstd.WithEncoderLevel(opt.Level.zstd()))
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(zw)

	err = tw.WriteHeader(&tar.Header{
		Typeflag:   tar.TypeXGlobalHeader,
		Name:       "pax_global_header",
		PAXRecords: map[string]string{totalKey: strconv.FormatInt(total, 10)},
	})
	if err != nil {
		return nil, err
	}

	prog := Progress{Total: total}
	report := func(name string) {
		prog.Current = name
		if opt.Progress != nil {
			opt.Progress(prog)
		}
	}

	for _, e := range entries {
		hdr, err := tar.FileInfoHeader(e.info, "")
		if err != nil {
			return nil, err
		}
		hdr.Name = e.name
		hdr.Format = tar.FormatPAX
		// Ownership and access times say nothing useful on another machine.
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
		hdr.AccessTime, hdr.ChangeTime = hdr.ModTime, hdr.ModTime

		if e.info.IsDir() {
			hdr.Name += "/"
			if err := tw.WriteHeader(hdr); err != nil {
				return nil, err
			}
			res.Dirs++
			res.Entries = append(res.Entries, Archived{Path: e.abs, IsDir: true})
			continue
		}
		report(e.name)
		if err := writeFile(tw, hdr, e.abs, &prog, opt.Progress); err != nil {
			return nil, err
		}
		res.Entries = append(res.Entries, Archived{Path: e.abs, Size: hdr.Size, ModTime: hdr.ModTime})
		res.Files++
		res.Bytes += hdr.Size
		prog.Files++
	}

	for _, c := range []io.Closer{tw, zw, seal} {
		if err := c.Close(); err != nil {
			return nil, err
		}
	}
	report("")
	return res, nil
}

func writeFile(tw *tar.Writer, hdr *tar.Header, abs string, prog *Progress, cb func(Progress)) error {
	f, err := os.Open(abs)
	if err != nil {
		return err
	}
	defer f.Close()
	// The size may have changed since the scan, so trust what is on disk now.
	st, err := f.Stat()
	if err != nil {
		return err
	}
	hdr.Size = st.Size()
	hdr.ModTime = st.ModTime()
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	pr := &countingReader{r: f, onRead: func(n int) {
		prog.Bytes += int64(n)
		if cb != nil {
			cb(*prog)
		}
	}}
	n, err := io.Copy(tw, io.LimitReader(pr, hdr.Size))
	if err != nil {
		return err
	}
	if n != hdr.Size {
		return fmt.Errorf("%s changed while it was being read", abs)
	}
	// Growth and in place edits do not show up in the byte count, so look at
	// the file again once it has been read.
	after, err := f.Stat()
	if err != nil {
		return err
	}
	if after.Size() != st.Size() || !after.ModTime().Equal(st.ModTime()) {
		return fmt.Errorf("%s changed while it was being read, try again when nothing is writing to it", abs)
	}
	return nil
}

type countingReader struct {
	r      io.Reader
	onRead func(int)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.onRead(n)
	}
	return n, err
}

// collect walks every selected path and returns the entries in archive order.
func collect(paths []string, res *PackResult, exclude []os.FileInfo) ([]entry, int64, error) {
	var out []entry
	var total int64
	seen := map[string]string{}
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, 0, err
		}
		base := filepath.Base(abs)
		if base == string(filepath.Separator) || base == "." || base == filepath.VolumeName(abs)+string(filepath.Separator) {
			return nil, 0, fmt.Errorf("cannot pack a filesystem root (%s), pick a folder or file inside it", abs)
		}
		key := strings.ToLower(base) // Windows and macOS treat names that differ by case as the same
		if prev, dup := seen[key]; dup {
			return nil, 0, fmt.Errorf("%s and %s would both be stored as %q, pack them separately", prev, abs, base)
		}
		seen[key] = abs

		err = filepath.WalkDir(abs, func(cur string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			rel, err := filepath.Rel(abs, cur)
			if err != nil {
				return err
			}
			name := base
			if rel != "." {
				name = base + "/" + filepath.ToSlash(rel)
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			for _, ex := range exclude {
				if os.SameFile(info, ex) {
					return nil
				}
			}
			switch {
			case info.IsDir(), info.Mode().IsRegular():
				out = append(out, entry{abs: cur, name: name, info: info})
				if info.Mode().IsRegular() {
					total += info.Size()
				}
			default:
				kind := "special file"
				if info.Mode()&fs.ModeSymlink != 0 {
					kind = "symbolic link"
				}
				res.Warnings = append(res.Warnings, fmt.Sprintf("skipped %s: %s", kind, cur))
			}
			return nil
		})
		if err != nil {
			return nil, 0, err
		}
	}
	return out, total, nil
}

func newBufReader(r io.Reader) *bufio.Reader { return bufio.NewReaderSize(r, 128*1024) }

// Printable makes an archive name safe to show in a terminal or page. Control
// characters, line breaks and direction overrides are written as escapes so
// a crafted name cannot move the cursor, fake extra lines or reorder text.
func Printable(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == unicode.ReplacementChar, unicode.IsControl(r), unicode.Is(unicode.Cf, r),
			r == '\u2028', r == '\u2029':
			fmt.Fprintf(&b, "\\u{%X}", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
