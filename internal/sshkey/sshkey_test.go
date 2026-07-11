package sshkey

import (
	"os"
	"strings"
	"testing"
)

func TestEnsureCreatesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	pub1, priv, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pub1, "ssh-ed25519 ") {
		t.Fatalf("pub line: %q", pub1)
	}
	st, err := os.Stat(priv)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("priv perm %v", st.Mode().Perm())
	}
	pub2, _, err := Ensure(dir) // second call must not regenerate
	if err != nil || pub2 != pub1 {
		t.Fatalf("not idempotent: %v / %q vs %q", err, pub2, pub1)
	}
}
