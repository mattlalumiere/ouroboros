// Command stubpack is a build helper. It compresses an unpacker the same way on
// every system and writes the checksum list the packer checks at run time, so
// the shell script and the Windows script give identical results and neither
// needs gzip or sha256sum installed.
//
//	stubpack gz <input> <output.gz>
//	stubpack manifest <folder>
package main

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "stubpack:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	switch {
	case len(args) == 3 && args[0] == "gz":
		return compress(args[1], args[2])
	case len(args) == 2 && args[0] == "manifest":
		return manifest(args[1])
	}
	return fmt.Errorf("usage: stubpack gz <input> <output.gz> | stubpack manifest <folder>")
}

// compress writes a gzip file with no name and no timestamp in its header, so
// the same input always gives the same bytes.
func compress(in, out string) error {
	raw, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	zw, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err != nil {
		f.Close()
		return err
	}
	if _, err := zw.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// manifest lists the SHA-256 of every unpacker-*.gz in dir.
func manifest(dir string) error {
	names, err := filepath.Glob(filepath.Join(dir, "unpacker-*.gz"))
	if err != nil {
		return err
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		raw, err := os.ReadFile(n)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), filepath.Base(n))
	}
	return os.WriteFile(filepath.Join(dir, "MANIFEST"), []byte(b.String()), 0o644)
}
