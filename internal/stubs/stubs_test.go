package stubs

import (
	"strings"
	"testing"
)

func TestVerifyDetectsTampering(t *testing.T) {
	// The manifest only exists after build.sh, so exercise verify directly.
	if _, err := files.ReadFile("bin/MANIFEST"); err != nil {
		t.Skip("no manifest, build.sh has not been run")
	}
	for _, tg := range Available() {
		raw, _ := files.ReadFile(entryName(tg))
		if err := verify(strings.TrimPrefix(entryName(tg), "bin/"), raw); err != nil {
			t.Errorf("%s: %v", tg, err)
		}
		bad := append([]byte{}, raw...)
		bad[len(bad)/2] ^= 1
		if err := verify(strings.TrimPrefix(entryName(tg), "bin/"), bad); err == nil {
			t.Errorf("%s: tampered stub was accepted", tg)
		}
	}
}
