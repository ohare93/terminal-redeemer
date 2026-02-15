package procmeta

import "testing"

func TestParseNullSeparated(t *testing.T) {
	t.Parallel()

	got := parseNullSeparated([]byte("zellij\x00attach\x00session-a\x00"))
	if len(got) != 3 || got[0] != "zellij" || got[2] != "session-a" {
		t.Fatalf("unexpected args: %#v", got)
	}
}

func TestParseEnv(t *testing.T) {
	t.Parallel()

	env := parseEnv([]byte("ZELLIJ_SESSION_NAME=my-sess\x00PWD=/tmp\x00"))
	if env["ZELLIJ_SESSION_NAME"] != "my-sess" {
		t.Fatalf("expected session name from env, got %#v", env)
	}
}

func TestParseParentPIDFromStat(t *testing.T) {
	t.Parallel()

	ppid, err := parseParentPIDFromStat("1234 (kitty) S 567 1 1 0 -1 4194304 0 0 0 0 0 0 0 0 20 0 1 0 0 0 0 0 0 0 0 0 0 0 0")
	if err != nil {
		t.Fatalf("parse ppid: %v", err)
	}
	if ppid != 567 {
		t.Fatalf("expected ppid 567, got %d", ppid)
	}
}
