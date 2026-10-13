package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/archive"
	"ouroboros/internal/stubs"
)

func TestOutputInsideInputIsRefusedEvenThroughLinks(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	os.MkdirAll(filepath.Join(data, "sub"), 0o755)
	link := filepath.Join(root, "lnk")
	if err := os.Symlink(filepath.Join(data, "sub"), link); err != nil {
		t.Skip("no symlinks here")
	}
	for _, dir := range []string{filepath.Join(data, "sub"), link} {
		_, err := outputPath(PackRequest{Paths: []string{data}, OutDir: dir, OutName: "x"}, stubs.Host())
		if err == nil || !strings.Contains(err.Error(), "being packed") {
			t.Errorf("outDir %s: err = %v", dir, err)
		}
	}
	if _, err := outputPath(PackRequest{Paths: []string{data}, OutDir: root, OutName: "fine"}, stubs.Host()); err != nil {
		t.Errorf("a folder outside the input must be allowed: %v", err)
	}
}

func TestKeyfileInsidePackedFolderIsDetected(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "src"), 0o755)
	key := filepath.Join(root, "src", "key.bin")
	os.WriteFile(key, []byte("k"), 0o600)
	if _, inside := insideAny(key, []string{filepath.Join(root, "src")}); !inside {
		t.Error("keyfile inside a packed folder was not detected")
	}
	if _, inside := insideAny(key, []string{filepath.Join(root, "other")}); inside {
		t.Error("false positive")
	}
}

func packed(t *testing.T, root string) *archive.PackResult {
	t.Helper()
	res, err := archive.Pack(&strings.Builder{}, []string{filepath.Join(root, "proj")}, archive.PackOptions{Passphrase: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestDeleteOnlyRemovesWhatWasArchived(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	os.MkdirAll(filepath.Join(proj, "sub"), 0o755)
	os.WriteFile(filepath.Join(proj, "a.txt"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(proj, "sub", "b.txt"), []byte("b"), 0o644)
	res := packed(t, root)

	// Something new appears after packing, and one archived file is edited.
	os.WriteFile(filepath.Join(proj, "sub", "new.txt"), []byte("new"), 0o644)
	os.WriteFile(filepath.Join(proj, "a.txt"), []byte("edited and longer"), 0o644)
	os.Chtimes(filepath.Join(proj, "a.txt"), time.Now().Add(time.Hour), time.Now().Add(time.Hour))

	warns := deleteArchived(res)
	if len(warns) == 0 {
		t.Error("expected warnings about kept files")
	}
	for _, keep := range []string{"a.txt", filepath.Join("sub", "new.txt")} {
		if _, err := os.Stat(filepath.Join(proj, keep)); err != nil {
			t.Errorf("%s should have been kept", keep)
		}
	}
	if _, err := os.Stat(filepath.Join(proj, "sub", "b.txt")); err == nil {
		t.Error("unchanged archived file should be deleted")
	}
}

func TestDeleteIsSkippedWhenEntriesWereSkipped(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	os.MkdirAll(proj, 0o755)
	os.WriteFile(filepath.Join(proj, "a.txt"), []byte("a"), 0o644)
	if err := os.Symlink("/etc", filepath.Join(proj, "link")); err != nil {
		t.Skip("no symlinks here")
	}
	res := packed(t, root)
	if len(res.Warnings) == 0 {
		t.Fatal("symlink should have produced a warning")
	}
	deleteArchived(res)
	for _, p := range []string{"a.txt", "link"} {
		if _, err := os.Lstat(filepath.Join(proj, p)); err != nil {
			t.Errorf("%s was deleted even though not everything was archived", p)
		}
	}
}
