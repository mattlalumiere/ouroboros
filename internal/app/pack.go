// Package app holds the packing and unpacking workflows used by both the
// browser interface and the command line mode.
package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

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

// Pack builds the self extracting executable described by req.
func Pack(req PackRequest, progress func(archive.Progress)) (string, []string, error) {
	if len(req.Paths) == 0 {
		return "", nil, errors.New("nothing selected to pack")
	}
	if req.Passphrase == "" {
		return "", nil, errors.New("a passphrase is required")
	}
	target := stubs.Target{OS: req.OS, Arch: req.Arch}
	if req.OS == "" {
		target = stubs.Host()
	}
	key, err := ReadKeyfile(req.Keyfile)
	if err != nil {
		return "", nil, err
	}

	out, err := outputPath(req, target)
	if err != nil {
		return "", nil, err
	}
	partial := out + ".partial"
	defer os.Remove(partial)

	stub, err := stubs.Open(target)
	if err != nil {
		return "", nil, err
	}
	f, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return "", nil, err
	}

	var res *archive.PackResult
	err = payload.Write(f, stub, func(w io.Writer) error {
		var perr error
		res, perr = archive.Pack(w, req.Paths, archive.PackOptions{
			Passphrase: req.Passphrase, Keyfile: key,
			Level: archive.Level(req.Level), Progress: progress,
		})
		return perr
	})
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
	if err := os.Chmod(partial, 0o755); err != nil {
		return "", nil, err
	}
	if err := os.Rename(partial, out); err != nil {
		return "", nil, err
	}

	warnings := res.Warnings
	if req.DeleteOriginals {
		for _, p := range req.Paths {
			if err := os.RemoveAll(p); err != nil {
				warnings = append(warnings, fmt.Sprintf("could not delete %s: %v", p, err))
			}
		}
	}
	st, _ := os.Stat(out)
	size := int64(0)
	if st != nil {
		size = st.Size()
	}
	return fmt.Sprintf("Created %s (%s). %d files and %d folders packed for %s.",
		out, humanSize(size), res.Files, res.Dirs, target), warnings, nil
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
	for _, p := range req.Paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		if rel, err := filepath.Rel(abs, out); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("the output would be inside %s, which is being packed. Save it somewhere else", abs)
		}
	}
	return out, nil
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
