package archive

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"
)

func TestHeaderSettingsAreBounded(t *testing.T) {
	good := packTree(t, makeTree(t), "pw", nil)
	cases := map[string]func(b []byte){
		"memory too large": func(b []byte) { b[10], b[11], b[12], b[13] = 0x00, 0x40, 0x00, 0x00 }, // 1 GiB
		"memory too small": func(b []byte) { b[10], b[11], b[12], b[13] = 0, 0, 0, 8 },
		"time too large":   func(b []byte) { b[9] = 255 },
		"threads too many": func(b []byte) { b[14] = 255 },
		"zero time":        func(b []byte) { b[9] = 0 },
	}
	for name, mutate := range cases {
		bad := append([]byte{}, good...)
		mutate(bad)
		_, err := Extract(bytes.NewReader(bad), t.TempDir(), ExtractOptions{Passphrase: "pw"})
		if err == nil || !strings.Contains(err.Error(), "outside the supported range") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestPassphraseUnicodeNormalisation(t *testing.T) {
	composed := norm.NFC.String("café pass")
	decomposed := norm.NFD.String("café pass")
	if composed == decomposed {
		t.Fatal("test strings should differ in bytes")
	}
	data := packTree(t, makeTree(t), composed, nil)
	if _, err := Extract(bytes.NewReader(data), t.TempDir(), ExtractOptions{Passphrase: decomposed}); err != nil {
		t.Errorf("decomposed form should unlock: %v", err)
	}
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
}

func TestOverwriteDoesNotFollowSymlinks(t *testing.T) {
	data := packTree(t, makeTree(t), "pw", nil)
	outside := filepath.Join(t.TempDir(), "victim.txt")
	os.WriteFile(outside, []byte("keep me"), 0o644)

	dst := t.TempDir()
	os.MkdirAll(filepath.Join(dst, "proj"), 0o755)
	symlinkOrSkip(t, outside, filepath.Join(dst, "proj", "a.txt"))

	_, err := Extract(bytes.NewReader(data), dst, ExtractOptions{Passphrase: "pw", Conflict: ConflictOverwrite})
	if err == nil {
		t.Error("overwriting through a link should be refused")
	}
	if read(t, outside) != "keep me" {
		t.Error("file outside the destination was modified")
	}
}

func TestSymlinkedFolderCannotRedirectExtraction(t *testing.T) {
	data := packTree(t, makeTree(t), "pw", nil)
	outsideDir := t.TempDir()

	for _, c := range []Conflict{ConflictRename, ConflictSkip, ConflictOverwrite} {
		dst := t.TempDir()
		symlinkOrSkip(t, outsideDir, filepath.Join(dst, "proj"))
		Extract(bytes.NewReader(data), dst, ExtractOptions{Passphrase: "pw", Conflict: c, KeepModes: true})
		if ents, _ := os.ReadDir(outsideDir); len(ents) != 0 {
			t.Fatalf("%s: files were written outside the destination", c)
		}
	}
}

func TestNoWideningOfPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits differ on Windows")
	}
	root := makeTree(t)
	os.Chmod(filepath.Join(root, "single.txt"), 0o600)
	data := packTree(t, root, "pw", nil)

	old := umaskSet(0o077)
	defer umaskSet(old)
	out := t.TempDir()
	if _, err := Extract(bytes.NewReader(data), out, ExtractOptions{Passphrase: "pw", KeepModes: false}); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(filepath.Join(out, "single.txt"))
	if st.Mode().Perm()&0o077 != 0 {
		t.Errorf("file mode %v is readable by others under umask 077", st.Mode().Perm())
	}
}

func TestExcludedFileIsNotPacked(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "d"), 0o755)
	os.WriteFile(filepath.Join(root, "d", "keep.txt"), []byte("k"), 0o644)
	self := filepath.Join(root, "d", "output.partial")
	os.WriteFile(self, []byte("being written"), 0o644)
	st, _ := os.Stat(self)

	var buf bytes.Buffer
	res, err := Pack(&buf, []string{filepath.Join(root, "d")}, PackOptions{Passphrase: "pw", Exclude: []os.FileInfo{st}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 1 {
		t.Errorf("files = %d, want 1", res.Files)
	}
}

func TestCaseInsensitiveDuplicateNames(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "x", "Data"), 0o755)
	os.MkdirAll(filepath.Join(root, "y", "data"), 0o755)
	_, err := Pack(&bytes.Buffer{}, []string{filepath.Join(root, "x", "Data"), filepath.Join(root, "y", "data")}, PackOptions{Passphrase: "pw"})
	if err == nil {
		t.Error("names differing only by case should be refused")
	}
}

func TestPrintableEscapesControlCharacters(t *testing.T) {
	got := Printable("a\x1b[31mred\nline‮evil")
	for _, bad := range []string{"\x1b", "\n", "‮"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q still contains %q", got, bad)
		}
	}
	if Printable("plain/file.txt") != "plain/file.txt" {
		t.Error("ordinary names must not change")
	}
}
