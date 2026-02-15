package config

import (
	"path/filepath"
	"testing"
)

func TestDefaultStateDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := DefaultStateDir()
	want := filepath.Join(home, ".terminal-redeemer")

	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}
