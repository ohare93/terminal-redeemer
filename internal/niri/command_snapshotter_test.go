package niri

import (
	"context"
	"errors"
	"testing"
)

func TestCommandSnapshotterUsesRunner(t *testing.T) {
	t.Parallel()

	want := []byte(`{"workspaces":[],"windows":[]}`)
	s := CommandSnapshotter{Command: "niri msg -j windows", Runner: stubRunner{out: want}}

	got, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestCommandSnapshotterError(t *testing.T) {
	t.Parallel()

	s := CommandSnapshotter{Command: "niri msg -j windows", Runner: stubRunner{err: errors.New("boom")}}
	if _, err := s.Snapshot(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

type stubRunner struct {
	out []byte
	err error
}

func (s stubRunner) Run(_ context.Context, _ string) ([]byte, error) {
	return s.out, s.err
}
