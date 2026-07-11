package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootHonorsEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UMBRA_ROOT", dir)
	if Root() != dir {
		t.Fatalf("Root() = %q, want %q", Root(), dir)
	}
	if got, want := APISocket(), filepath.Join(dir, "run", "api.sock"); got != want {
		t.Fatalf("APISocket() = %q, want %q", got, want)
	}
}

func TestEnsureTreeCreatesAllDirs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UMBRA_ROOT", dir)
	if err := EnsureTree(); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{Machines(), Images(), Run(), Logs(), SSH()} {
		st, err := os.Stat(d)
		if err != nil || !st.IsDir() {
			t.Fatalf("missing dir %s: %v", d, err)
		}
		if st.Mode().Perm() != 0o700 {
			t.Fatalf("%s perm = %v, want 0700", d, st.Mode().Perm())
		}
	}
}
