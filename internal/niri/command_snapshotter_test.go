package niri

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestCommandSnapshotterUsesRunner(t *testing.T) {
	t.Parallel()

	runner := stubRunner{responses: map[string]stubResult{
		"niri msg -j windows":    {out: []byte(`[{"id":1}]`)},
		"niri msg -j workspaces": {out: []byte(`[{"id":2,"idx":1,"name":null}]`)},
	}}
	s := CommandSnapshotter{Command: "niri msg -j windows", Runner: runner}

	got, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(got, &payload); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if _, ok := payload["workspaces"]; !ok {
		t.Fatalf("expected combined payload to contain workspaces, got %q", got)
	}
	if _, ok := payload["windows"]; !ok {
		t.Fatalf("expected combined payload to contain windows, got %q", got)
	}
}

func TestCommandSnapshotterError(t *testing.T) {
	t.Parallel()

	s := CommandSnapshotter{Command: "niri msg -j windows", Runner: stubRunner{err: errors.New("boom")}}
	if _, err := s.Snapshot(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestCommandSnapshotterFallsBackToWindowsOnlyWhenWorkspacesCommandFails(t *testing.T) {
	t.Parallel()

	runner := stubRunner{responses: map[string]stubResult{
		"niri msg -j windows":    {out: []byte(`[{"id":1}]`)},
		"niri msg -j workspaces": {err: errors.New("nope")},
	}}
	s := CommandSnapshotter{Command: "niri msg -j windows", Runner: runner}

	got, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if string(got) != `[{"id":1}]` {
		t.Fatalf("expected windows-only fallback payload, got %q", got)
	}
}

type stubRunner struct {
	out       []byte
	err       error
	responses map[string]stubResult
}

type stubResult struct {
	out []byte
	err error
}

func (s stubRunner) Run(_ context.Context, command string) ([]byte, error) {
	if s.responses != nil {
		if result, ok := s.responses[command]; ok {
			return result.out, result.err
		}
		return nil, errors.New("missing stub response")
	}
	return s.out, s.err
}
