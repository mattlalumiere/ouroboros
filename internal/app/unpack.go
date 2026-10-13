package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"ouroboros/internal/archive"
	"ouroboros/internal/payload"
)

// ExtractRequest describes one unpacking job.
type ExtractRequest struct {
	Passphrase string `json:"passphrase"`
	Keyfile    string `json:"keyfile"`
	Dest       string `json:"dest"`
	Conflict   string `json:"conflict"`
	KeepModes  bool   `json:"keepModes"`
}

// SelfPath is the path of the running executable with links resolved.
func SelfPath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p, nil
}

// DefaultDest suggests a folder next to the executable named after it.
func DefaultDest() string {
	self, err := SelfPath()
	if err != nil {
		return ""
	}
	base := strings.TrimSuffix(filepath.Base(self), filepath.Ext(self))
	return filepath.Join(filepath.Dir(self), base+"-extracted")
}

// withPayload opens the archive attached to this executable.
func withPayload(fn func(io.Reader) error) error {
	self, err := SelfPath()
	if err != nil {
		return err
	}
	sec, f, err := payload.Open(self)
	if err != nil {
		return err
	}
	defer f.Close()
	return fn(sec)
}

// NeedsKeyfile reports whether the attached archive was sealed with a keyfile.
func NeedsKeyfile() (bool, error) {
	var need bool
	err := withPayload(func(r io.Reader) (e error) {
		need, e = archive.NeedsKeyfile(r)
		return
	})
	return need, err
}

// List decrypts the attached archive and returns what it contains.
func List(pass, keyfile string) ([]archive.Item, error) {
	kdf.Lock()
	defer kdf.Unlock()
	key, err := ReadKeyfile(keyfile)
	if err != nil {
		return nil, err
	}
	var items []archive.Item
	err = withPayload(func(r io.Reader) (e error) {
		items, e = archive.List(r, pass, key)
		return
	})
	return items, err
}

// Extract unpacks the attached archive.
func Extract(req ExtractRequest, progress func(archive.Progress)) (string, []string, error) {
	kdf.Lock()
	defer kdf.Unlock()
	key, err := ReadKeyfile(req.Keyfile)
	if err != nil {
		return "", nil, err
	}
	if req.Dest == "" {
		return "", nil, fmt.Errorf("choose a destination folder")
	}
	dest, err := filepath.Abs(req.Dest)
	if err != nil {
		return "", nil, err
	}
	var res *archive.ExtractResult
	err = withPayload(func(r io.Reader) (e error) {
		res, e = archive.Extract(r, dest, archive.ExtractOptions{
			Passphrase: req.Passphrase, Keyfile: key,
			Conflict: archive.Conflict(req.Conflict), KeepModes: req.KeepModes,
			Progress: progress,
		})
		return
	})
	if err != nil {
		return "", nil, err
	}
	msg := fmt.Sprintf("Extracted %d files (%s) and %d folders to %s.", res.Files, humanSize(res.Bytes), res.Dirs, dest)
	if res.Skipped > 0 {
		msg += fmt.Sprintf(" %d existing files were skipped.", res.Skipped)
	}
	return msg, res.Warnings, nil
}
