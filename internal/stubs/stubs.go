// Package stubs holds the prebuilt unpacker programs for every supported
// platform. They are produced by build.sh and embedded into the packer.
package stubs

import (
	"bytes"
	"compress/gzip"
	"embed"
	"fmt"
	"io"
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

// Open returns the uncompressed unpacker program for t.
func Open(t Target) (io.Reader, error) {
	raw, err := files.ReadFile(entryName(t))
	if err != nil {
		return nil, fmt.Errorf("no unpacker is embedded for %s, rebuild with build.sh", t)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	return zr, nil
}
