package procmeta

import (
	"errors"
	"testing"

	"github.com/jmo/terminal-redeemer/internal/model"
)

func TestCWDExtractionBehavior(t *testing.T) {
	t.Parallel()

	reader := stubReader{byPID: map[int]ProcessInfo{4242: {CWD: "/home/jmo/Development/active/tools"}}}
	enricher := NewEnricher(reader, Config{})

	window := model.Window{Key: "w-1", AppID: "kitty", PID: 4242}
	got, err := enricher.EnrichWindow(window)
	if err != nil {
		t.Fatalf("enrich window: %v", err)
	}

	if got.Terminal == nil || got.Terminal.CWD != "/home/jmo/Development/active/tools" {
		t.Fatalf("expected cwd in terminal metadata, got %#v", got.Terminal)
	}
}

func TestWhitelistProcessTagsDefaultAndExtras(t *testing.T) {
	t.Parallel()

	reader := stubReader{byPID: map[int]ProcessInfo{4242: {ProcessChain: []string{"zsh", "opencode", "claude", "htop", "mytool"}}}}
	enricher := NewEnricher(reader, Config{WhitelistExtra: []string{"mytool"}})

	window := model.Window{Key: "w-1", AppID: "kitty", PID: 4242}
	got, err := enricher.EnrichWindow(window)
	if err != nil {
		t.Fatalf("enrich window: %v", err)
	}

	if got.Terminal == nil {
		t.Fatal("expected terminal metadata")
	}
	tags := got.Terminal.ProcessTags
	if len(tags) != 3 || tags[0] != "claude" || tags[1] != "mytool" || tags[2] != "opencode" {
		t.Fatalf("unexpected process tags: %#v", tags)
	}
}

func TestSessionTagExtractionBestEffort(t *testing.T) {
	t.Parallel()

	reader := stubReader{byPID: map[int]ProcessInfo{4242: {
		Args: []string{"zellij", "attach", "redeemer"},
		Env:  map[string]string{"ZELLIJ_SESSION_NAME": "from-env"},
	}}}
	enricher := NewEnricher(reader, Config{IncludeSessionTag: true})

	window := model.Window{Key: "w-1", AppID: "kitty", PID: 4242, Title: "terminal [session:title]"}
	got, err := enricher.EnrichWindow(window)
	if err != nil {
		t.Fatalf("enrich window: %v", err)
	}

	if got.Terminal == nil || got.Terminal.SessionTag != "from-env" {
		t.Fatalf("expected env-priority session tag, got %#v", got.Terminal)
	}
}

func TestNonTerminalWindowUnchanged(t *testing.T) {
	t.Parallel()

	enricher := NewEnricher(stubReader{}, Config{IncludeSessionTag: true})
	window := model.Window{Key: "w-2", AppID: "firefox", PID: 3333}
	got, err := enricher.EnrichWindow(window)
	if err != nil {
		t.Fatalf("enrich window: %v", err)
	}
	if got.Terminal != nil {
		t.Fatalf("expected nil terminal metadata for non-terminal app, got %#v", got.Terminal)
	}
}

func TestReaderFailureReturnsError(t *testing.T) {
	t.Parallel()

	enricher := NewEnricher(stubReader{err: errors.New("boom")}, Config{})
	window := model.Window{Key: "w-1", AppID: "kitty", PID: 4242}
	_, err := enricher.EnrichWindow(window)
	if err == nil {
		t.Fatal("expected inspect error")
	}
}

type stubReader struct {
	byPID map[int]ProcessInfo
	err   error
}

func (s stubReader) Inspect(pid int) (ProcessInfo, error) {
	if s.err != nil {
		return ProcessInfo{}, s.err
	}
	if info, ok := s.byPID[pid]; ok {
		return info, nil
	}
	return ProcessInfo{}, nil
}
