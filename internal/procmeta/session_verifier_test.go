package procmeta

import (
	"errors"
	"testing"
)

func TestZellijSessionVerifierExists(t *testing.T) {
	t.Parallel()

	v := NewZellijSessionVerifier(stubCommandExec{out: []byte("sensible-bee\njoyous-galaxy\n")})
	ok, err := v.Exists("sensible-bee")
	if err != nil {
		t.Fatalf("exists: %v", err)
	}
	if !ok {
		t.Fatal("expected session to exist")
	}
}

func TestZellijSessionVerifierListDeduplicatesNames(t *testing.T) {
	t.Parallel()

	v := NewZellijSessionVerifier(stubCommandExec{out: []byte("sensible-bee (current)\nsensible-bee\njoyous-galaxy\n")})
	got, err := v.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 || got[0] != "sensible-bee" || got[1] != "joyous-galaxy" {
		t.Fatalf("sessions = %#v", got)
	}
}

func TestParseZellijSessionsIgnoresNoActiveMessage(t *testing.T) {
	t.Parallel()
	if got := ParseZellijSessions([]byte("No active zellij sessions found\n")); len(got) != 0 {
		t.Fatalf("sessions = %#v", got)
	}
}

func TestZellijSessionVerifierMissing(t *testing.T) {
	t.Parallel()

	v := NewZellijSessionVerifier(stubCommandExec{out: []byte("sensible-bee\n")})
	ok, err := v.Exists("unknown")
	if err != nil {
		t.Fatalf("exists: %v", err)
	}
	if ok {
		t.Fatal("expected session to be missing")
	}
}

func TestZellijSessionVerifierError(t *testing.T) {
	t.Parallel()

	v := NewZellijSessionVerifier(stubCommandExec{err: errors.New("boom")})
	_, err := v.Exists("sensible-bee")
	if err == nil {
		t.Fatal("expected error")
	}
}

type stubCommandExec struct {
	out []byte
	err error
}

func (s stubCommandExec) Output(_ string, _ ...string) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.out, nil
}
