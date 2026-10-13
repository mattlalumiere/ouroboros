package archive

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func makeTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "proj", "sub", "deep"), 0o755))
	must(os.MkdirAll(filepath.Join(root, "proj", "empty"), 0o755))
	must(os.WriteFile(filepath.Join(root, "proj", "a.txt"), []byte("hello"), 0o644))
	must(os.WriteFile(filepath.Join(root, "proj", "sub", "b.bin"), bytes.Repeat([]byte{1, 2, 3}, 100000), 0o600))
	must(os.WriteFile(filepath.Join(root, "proj", "sub", "deep", "c.txt"), nil, 0o644))
	must(os.WriteFile(filepath.Join(root, "single.txt"), []byte("solo"), 0o644))
	return root
}

func packTree(t *testing.T, root, pass string, key []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	_, err := Pack(&buf, []string{filepath.Join(root, "proj"), filepath.Join(root, "single.txt")},
		PackOptions{Passphrase: pass, Keyfile: key, Level: LevelFast})
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRoundTripKeepsStructure(t *testing.T) {
	root := makeTree(t)
	data := packTree(t, root, "correct horse", nil)

	out := t.TempDir()
	res, err := Extract(bytes.NewReader(data), out, ExtractOptions{Passphrase: "correct horse", KeepModes: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 4 {
		t.Errorf("files = %d, want 4", res.Files)
	}
	if read(t, filepath.Join(out, "proj", "a.txt")) != "hello" ||
		read(t, filepath.Join(out, "single.txt")) != "solo" ||
		len(read(t, filepath.Join(out, "proj", "sub", "b.bin"))) != 300000 {
		t.Error("file contents differ")
	}
	if st, err := os.Stat(filepath.Join(out, "proj", "empty")); err != nil || !st.IsDir() {
		t.Error("empty directory was not kept")
	}
	// Windows has no Unix permission bits to compare.
	if st, _ := os.Stat(filepath.Join(out, "proj", "sub", "b.bin")); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
}

func TestWrongPassphrase(t *testing.T) {
	data := packTree(t, makeTree(t), "right", nil)
	_, err := Extract(bytes.NewReader(data), t.TempDir(), ExtractOptions{Passphrase: "wrong"})
	if err != ErrWrongKey {
		t.Fatalf("err = %v, want ErrWrongKey", err)
	}
}

func TestKeyfile(t *testing.T) {
	key := []byte("keyfile contents")
	data := packTree(t, makeTree(t), "pw", key)
	if need, _ := NeedsKeyfile(bytes.NewReader(data)); !need {
		t.Error("keyfile flag not set")
	}
	if _, err := Extract(bytes.NewReader(data), t.TempDir(), ExtractOptions{Passphrase: "pw"}); err == nil {
		t.Error("extract without keyfile should fail")
	}
	if _, err := Extract(bytes.NewReader(data), t.TempDir(), ExtractOptions{Passphrase: "pw", Keyfile: []byte("other")}); err != ErrWrongKey {
		t.Errorf("wrong keyfile err = %v", err)
	}
	if _, err := Extract(bytes.NewReader(data), t.TempDir(), ExtractOptions{Passphrase: "pw", Keyfile: key}); err != nil {
		t.Errorf("right keyfile: %v", err)
	}
}

func TestTamperAndTruncation(t *testing.T) {
	data := packTree(t, makeTree(t), "pw", nil)
	opt := ExtractOptions{Passphrase: "pw"}

	flipped := append([]byte{}, data...)
	flipped[len(flipped)/2] ^= 0xff
	if _, err := Extract(bytes.NewReader(flipped), t.TempDir(), opt); err == nil {
		t.Error("flipped byte went unnoticed")
	}

	// Cutting the stream at a chunk boundary must not look like a clean end.
	if _, err := Extract(bytes.NewReader(data[:len(data)-20]), t.TempDir(), opt); err == nil {
		t.Error("truncation went unnoticed")
	}
}

func TestConflictPolicies(t *testing.T) {
	data := packTree(t, makeTree(t), "pw", nil)
	out := t.TempDir()
	run := func(c Conflict) *ExtractResult {
		res, err := Extract(bytes.NewReader(data), out, ExtractOptions{Passphrase: "pw", Conflict: c})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	run(ConflictOverwrite)
	os.WriteFile(filepath.Join(out, "single.txt"), []byte("mine"), 0o644)

	if r := run(ConflictSkip); r.Skipped != 4 || read(t, filepath.Join(out, "single.txt")) != "mine" {
		t.Errorf("skip: %+v", r)
	}
	run(ConflictRename)
	if read(t, filepath.Join(out, "single (2).txt")) != "solo" || read(t, filepath.Join(out, "single.txt")) != "mine" {
		t.Error("rename did not keep both files")
	}
	run(ConflictOverwrite)
	if read(t, filepath.Join(out, "single.txt")) != "solo" {
		t.Error("overwrite did not replace file")
	}
}

func TestListAndEmptyArchive(t *testing.T) {
	data := packTree(t, makeTree(t), "pw", nil)
	items, err := List(bytes.NewReader(data), "pw", nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, i := range items {
		names = append(names, i.Path)
	}
	if !strings.Contains(strings.Join(names, ","), "proj/sub/deep/c.txt") {
		t.Errorf("listing: %v", names)
	}
	// File names must not appear in the clear.
	if bytes.Contains(data, []byte("c.txt")) || bytes.Contains(data, []byte("hello")) {
		t.Error("plaintext leaked into archive")
	}
}

func TestSafeJoinRejectsEscapes(t *testing.T) {
	for _, bad := range []string{"../evil", "a/../../evil", "/etc/passwd", "..", "."} {
		if _, err := safeJoin("/dst", bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if _, err := safeJoin("/dst", "ok/sub/file"); err != nil {
		t.Error(err)
	}
}

func TestExtractRefusesTraversalEntry(t *testing.T) {
	var buf bytes.Buffer
	seal, _ := newSealer(&buf, "pw", nil)
	zw, _ := zstd.NewWriter(seal)
	tw := tar.NewWriter(zw)
	tw.WriteHeader(&tar.Header{Name: "../evil.txt", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
	tw.Write([]byte("x"))
	tw.Close()
	zw.Close()
	seal.Close()

	parent := t.TempDir()
	_, err := Extract(&buf, filepath.Join(parent, "dst"), ExtractOptions{Passphrase: "pw"})
	if err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Errorf("err = %v, want unsafe path", err)
	}
	if _, err := os.Stat(filepath.Join(parent, "evil.txt")); err == nil {
		t.Error("file escaped destination")
	}
}
