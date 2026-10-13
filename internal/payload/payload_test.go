package payload

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestAttachAndFind(t *testing.T) {
	p := filepath.Join(t.TempDir(), "prog")
	var buf bytes.Buffer
	err := Write(&buf, bytes.NewReader([]byte("fake executable")), func(w io.Writer) error {
		_, err := w.Write([]byte("secret payload"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(p, buf.Bytes(), 0o755)

	sec, f, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, _ := io.ReadAll(sec)
	if string(got) != "secret payload" {
		t.Errorf("payload = %q", got)
	}
}

func TestNoPayload(t *testing.T) {
	p := filepath.Join(t.TempDir(), "plain")
	os.WriteFile(p, bytes.Repeat([]byte("x"), 100), 0o755)
	if _, _, err := Open(p); err != ErrNone {
		t.Errorf("err = %v, want ErrNone", err)
	}
	os.WriteFile(p, []byte("tiny"), 0o755)
	if _, _, err := Open(p); err != ErrNone {
		t.Errorf("tiny file err = %v, want ErrNone", err)
	}
}
