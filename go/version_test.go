package mantyx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	v := Version()
	if v == "" {
		t.Fatal("Version() is empty")
	}
	if strings.TrimSpace(v) != v {
		t.Fatalf("Version() has surrounding whitespace: %q", v)
	}
	root, err := findRepoRoot(t)
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		t.Fatalf("read VERSION: %v", err)
	}
	want := strings.TrimSpace(string(raw))
	if v != want {
		t.Fatalf("Version() = %q, want %q (from VERSION file)", v, want)
	}
}

func findRepoRoot(t *testing.T) (string, error) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "VERSION")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
