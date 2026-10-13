// Package stubs holds the prebuilt unpacker programs for every supported
// platform. They are produced by build.sh and embedded into the packer.
package stubs

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"runtime"
	"sort"
	"strings"
)

//go:embed bin
var files embed.FS

// Target is an operating system and CPU architecture pair.
type Target struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

func (t Target) String() string { return t.OS + "/" + t.Arch }

// Ext is the file extension executables of this target use.
func (t Target) Ext() string {
	if t.OS == "windows" {
		return ".exe"
	}
	return ""
}

// Host is the target the packer itself is running on.
func Host() Target { return Target{runtime.GOOS, runtime.GOARCH} }

func entryName(t Target) string { return fmt.Sprintf("bin/unpacker-%s-%s.gz", t.OS, t.Arch) }

// Available lists every target that has an embedded stub.
func Available() []Target {
	ents, _ := files.ReadDir("bin")
	var out []Target
	for _, e := range ents {
		n := e.Name()
		if !strings.HasPrefix(n, "unpacker-") || !strings.HasSuffix(n, ".gz") {
			continue
		}
		parts := strings.SplitN(strings.TrimSuffix(strings.TrimPrefix(n, "unpacker-"), ".gz"), "-", 2)
		if len(parts) == 2 {
			out = append(out, Target{parts[0], parts[1]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// Open returns the uncompressed unpacker program for t. The embedded file is
// checked against the manifest written by build.sh first, which catches a
// stale or hand edited stub before it is copied into someone's output.
func Open(t Target) (io.Reader, error) {
	raw, err := files.ReadFile(entryName(t))
	if err != nil {
		return nil, fmt.Errorf("no unpacker is embedded for %s, rebuild with build.sh", t)
	}
	if err := verify(path.Base(entryName(t)), raw); err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	return zr, nil
}

// verify compares raw with the SHA-256 recorded for name in the manifest.
func verify(name string, raw []byte) error {
	manifest, err := files.ReadFile("bin/MANIFEST")
	if err != nil {
		return errors.New("the unpacker manifest is missing, rebuild with build.sh")
	}
	sum := sha256.Sum256(raw)
	want := hex.EncodeToString(sum[:])
	for _, line := range strings.Split(string(manifest), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			if f[0] == want {
				return nil
			}
			return fmt.Errorf("the embedded unpacker %s does not match its recorded checksum, rebuild with build.sh", name)
		}
	}
	return fmt.Errorf("the embedded unpacker %s is not listed in the manifest, rebuild with build.sh", name)
}
