// Package app holds the packing and unpacking workflows used by both the
// browser interface and the command line mode.
package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"ouroboros/internal/archive"
	"ouroboros/internal/payload"
	"ouroboros/internal/stubs"
)

// PackRequest describes one packing job.
type PackRequest struct {
	Paths           []string `json:"paths"`
	Passphrase      string   `json:"passphrase"`
	Keyfile         string   `json:"keyfile"`
	Level           string   `json:"level"`
	OS              string   `json:"os"`
	Arch            string   `json:"arch"`
	OutDir          string   `json:"outDir"`
	OutName         string   `json:"outName"`
	DeleteOriginals bool     `json:"deleteOriginals"`
}

// ReadKeyfile loads a keyfile, or returns nil when no path is given.
func ReadKeyfile(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read keyfile: %w", err)
	}
	if len(b) == 0 {
		return nil, errors.New("the keyfile is empty")
	}
	return b, nil
}

// partials tracks unfinished output files so an interrupt can remove them.
var partials sync.Map

// kdf serialises work that runs the memory hungry key derivation, so clicking
// twice or calling the API in parallel cannot exhaust memory.
var kdf sync.Mutex

// Cleanup removes any unfinished output. Call it before exiting on a signal.
func Cleanup() {
	partials.Range(func(k, _ any) bool {
		os.Remove(k.(string))
		return true
	})
}

// Pack builds the self extracting executable described by req.
func Pack(req PackRequest, progress func(archive.Progress)) (string, []string, error) {
	if len(req.Paths) == 0 {
		return "", nil, errors.New("nothing selected to pack")
	}
	if req.Passphrase == "" {
		return "", nil, errors.New("a passphrase is required")
	}
	kdf.Lock()
	defer kdf.Unlock()
	target := stubs.Target{OS: req.OS, Arch: req.Arch}
	if req.OS == "" {
		target = stubs.Host()
	}
	key, err := ReadKeyfile(req.Keyfile)
	if err != nil {
		return "", nil, err
	}
	if req.Keyfile != "" {
		if root, inside := insideAny(req.Keyfile, req.Paths); inside {
			return "", nil, fmt.Errorf("the keyfile is inside %s, which is being packed. The archive would contain its own key", root)
		}
	}

	out, err := outputPath(req, target)
	if err != nil {
		return "", nil, err
	}
	stub, err := stubs.Open(target)
	if err != nil {
		return "", nil, err
	}

	// A random name created exclusively, so nothing planted in the output
	// folder can be followed or overwritten.
	f, err := os.CreateTemp(filepath.Dir(out), filepath.Base(out)+".*.partial")
	if err != nil {
		return "", nil, err
	}
	partial := f.Name()
	partials.Store(partial, true)
	defer func() {
		partials.Delete(partial)
		os.Remove(partial)
	}()
	self, err := f.Stat()
	if err != nil {
		f.Close()
		return "", nil, err
	}

	var res *archive.PackResult
	err = payload.Write(f, stub, func(w io.Writer) error {
		var perr error
		res, perr = archive.Pack(w, req.Paths, archive.PackOptions{
			Passphrase: req.Passphrase, Keyfile: key,
			Level: archive.Level(req.Level), Progress: progress,
			Exclude: []os.FileInfo{self},
		})
		return perr
	})
	if err == nil {
		err = f.Chmod(0o755)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", nil, err
	}

	// Read the whole archive back before trusting it or touching the originals.
	sec, vf, err := payload.Open(partial)
	if err != nil {
		return "", nil, fmt.Errorf("verification failed: %w", err)
	}
	_, err = archive.List(sec, req.Passphrase, key)
	vf.Close()
	if err != nil {
		return "", nil, fmt.Errorf("verification failed: %w", err)
	}

	// A hard link fails if the name is taken, unlike rename, which would
	// silently replace something created since the earlier check.
	if err := os.Link(partial, out); err != nil {
		if _, serr := os.Lstat(out); serr == nil {
			return "", nil, fmt.Errorf("%s already exists, pick another name", out)
		}
		if err := os.Rename(partial, out); err != nil {
			return "", nil, err
		}
	}

	warnings := res.Warnings
	if req.DeleteOriginals {
		warnings = append(warnings, deleteArchived(res)...)
	}
	sum, size := fingerprint(out)
	return fmt.Sprintf("Created %s (%s). %d files and %d folders packed for %s.\nSHA-256: %s",
		out, humanSize(size), res.Files, res.Dirs, target, sum), warnings, nil
}

// deleteArchived removes only what was stored in the archive, and only files
// that are exactly as they were when packed. Folders go last and only when
// they are empty, so anything that was skipped or added since stays put.
func deleteArchived(res *archive.PackResult) []string {
	if len(res.Warnings) > 0 {
		return []string{"nothing was deleted, because some entries were skipped and are not in the archive"}
	}
	var warns []string
	var dirs []string
	for _, e := range res.Entries {
		if e.IsDir {
			dirs = append(dirs, e.Path)
			continue
		}
		st, err := os.Lstat(e.Path)
		if err != nil {
			continue
		}
		if !st.Mode().IsRegular() || st.Size() != e.Size || !st.ModTime().Equal(e.ModTime) {
			warns = append(warns, fmt.Sprintf("kept %s, it changed after it was packed", e.Path))
			continue
		}
		if err := os.Remove(e.Path); err != nil {
			warns = append(warns, fmt.Sprintf("could not delete %s: %v", e.Path, err))
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, d := range dirs {
		if err := os.Remove(d); err != nil {
			warns = append(warns, fmt.Sprintf("kept folder %s, it still has files that were not archived", d))
		}
	}
	return warns
}

// fingerprint returns the SHA-256 of a file, for sharing over a second channel.
func fingerprint(path string) (string, int64) {
	f, err := os.Open(path)
	if err != nil {
		return "unavailable", 0
	}
	defer f.Close()
	h := sha256.New()
	n, _ := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n
}

func outputPath(req PackRequest, target stubs.Target) (string, error) {
	dir := req.OutDir
	if dir == "" {
		return "", errors.New("choose where to save the executable")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return "", fmt.Errorf("%s is not a folder", dir)
	}
	name := strings.TrimSpace(req.OutName)
	if name == "" {
		name = "secure-archive"
	}
	if name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
		return "", errors.New("the file name must not contain folder separators")
	}
	if ext := target.Ext(); ext != "" && !strings.HasSuffix(strings.ToLower(name), ext) {
		name += ext
	}
	out := filepath.Join(dir, name)

	if _, err := os.Lstat(out); err == nil {
		return "", fmt.Errorf("%s already exists, pick another name", out)
	}
	// An output file inside a packed folder would end up packing itself.
	if root, inside := insideAny(out, req.Paths); inside {
		return "", fmt.Errorf("the output would be inside %s, which is being packed. Save it somewhere else", root)
	}
	return out, nil
}

// insideAny reports whether p is one of the roots or below one of them, after
// resolving links, and on systems where names are case insensitive, ignoring case.
func insideAny(p string, roots []string) (string, bool) {
	rp := resolve(p)
	for _, r := range roots {
		rr := resolve(r)
		if rel, err := filepath.Rel(rr, rp); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return r, true
		}
	}
	return "", false
}

// resolve gives an absolute, link free path. The last part may not exist yet.
func resolve(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	dir, base := filepath.Split(abs)
	if d, err := filepath.EvalSymlinks(dir); err == nil {
		dir = d
	}
	full := filepath.Join(dir, base)
	if f, err := filepath.EvalSymlinks(full); err == nil {
		full = f
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		full = strings.ToLower(full)
	}
	return full
}

func humanSize(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}
