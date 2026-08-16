package mirror

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestParseProjectionSSHArgvExactDeterministicForms(t *testing.T) {
	for _, tc := range []struct {
		name    string
		session string
		create  bool
	}{
		{name: "attach", session: "Alpha"},
		{name: "create", session: "redeem-0123456789abcdef0123456789abcdef", create: true},
		{name: "leading dash", session: "-hostile'; exec echo NO"},
		{name: "case distinct", session: "alpha"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			window := Window{Order: 1, Title: "ignored", ZellijSession: tc.session, Terminal: &Terminal{CWD: "/tmp/a'b"}}
			cfg := LaunchConfig{SourceHost: "user@lattice", SSHCommand: "/usr/bin/ssh", LauncherCommand: "kitty", AppID: "owned"}
			var plan LaunchPlan
			var err error
			if tc.create {
				plan, err = PlanNew(tc.session, cfg)
			} else {
				plan, err = PlanLaunch(window, cfg)
			}
			if err != nil {
				t.Fatal(err)
			}
			argv := plan.Command.Args[len(plan.Command.Args)-5:]
			argv = append([]string{"/usr/bin/ssh"}, argv[1:]...)
			host, session, ok := ParseProjectionSSHArgv(argv, "/usr/bin/ssh")
			if !ok || host != "user@lattice" || session != tc.session {
				t.Fatalf("parse=(%q,%q,%v) argv=%#v", host, session, ok, argv)
			}
		})
	}
}

func TestParseProjectionSSHArgvRejectsNearMatches(t *testing.T) {
	plan, err := PlanLaunch(Window{ZellijSession: "Alpha"}, LaunchConfig{SourceHost: "lattice", SSHCommand: "ssh", LauncherCommand: "kitty", AppID: "owned"})
	if err != nil {
		t.Fatal(err)
	}
	base := append([]string{"ssh"}, plan.Command.Args[len(plan.Command.Args)-4:]...)
	cases := [][]string{
		append([]string(nil), base[:len(base)-1]...),
		{"ssh", "-tt", "--", "lattice", "zellij attach Alpha"},
		append([]string{"other-ssh"}, base[1:]...),
	}
	for _, argv := range cases {
		if _, _, ok := ParseProjectionSSHArgv(argv, "ssh"); ok {
			t.Fatalf("accepted malformed argv %#v", argv)
		}
	}
	near := append([]string(nil), base...)
	near[len(near)-1] = strings.Replace(near[len(near)-1], "'Alpha'", "'Alpha-extra'", 1)
	_, session, ok := ParseProjectionSSHArgv(near, "ssh")
	if !ok || session != "Alpha-extra" || session == "Alpha" {
		t.Fatalf("substring extraction was not exact: session=%q ok=%v", session, ok)
	}
}

func TestProcProjectionInspectorFailsClosedForAmbiguousAndTitleOnly(t *testing.T) {
	root := t.TempDir()
	writeMirrorProc(t, root, 100, 1, 10, []string{"kitty"})
	planA, _ := PlanLaunch(Window{ZellijSession: "A"}, LaunchConfig{SourceHost: "lattice", SSHCommand: "ssh", LauncherCommand: "kitty", AppID: "owned"})
	planB, _ := PlanLaunch(Window{ZellijSession: "B"}, LaunchConfig{SourceHost: "lattice", SSHCommand: "ssh", LauncherCommand: "kitty", AppID: "owned"})
	writeMirrorProc(t, root, 101, 100, 11, append([]string{"ssh"}, planA.Command.Args[len(planA.Command.Args)-4:]...))
	writeMirrorProc(t, root, 102, 100, 12, append([]string{"ssh"}, planB.Command.Args[len(planB.Command.Args)-4:]...))
	writeMirrorProc(t, root, 200, 1, 20, []string{"kitty"})

	inventory, err := (ProcProjectionInspector{ProcRoot: root, SSHCommand: "ssh"}).Inspect(context.Background(), []OwnedWindow{
		{ID: 1, PID: 100, AppID: "owned", Title: "lattice[0|A]: A"},
		{ID: 2, PID: 200, AppID: "owned", Title: "lattice[0|A]: A"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Exact) != 0 || len(inventory.Untracked) != 2 {
		t.Fatalf("inventory=%#v", inventory)
	}
}

func TestProcProjectionInspectorTracksPreUpgradeTitleByEvidence(t *testing.T) {
	root := t.TempDir()
	writeMirrorProc(t, root, 100, 1, 10, []string{"kitty"})
	plan, _ := PlanLaunch(Window{ZellijSession: "Case"}, LaunchConfig{SourceHost: "lattice", SSHCommand: "ssh", LauncherCommand: "kitty", AppID: "owned"})
	writeMirrorProc(t, root, 101, 100, 11, append([]string{"ssh"}, plan.Command.Args[len(plan.Command.Args)-4:]...))
	inventory, err := (ProcProjectionInspector{ProcRoot: root, SSHCommand: "ssh"}).Inspect(context.Background(), []OwnedWindow{{ID: 1, PID: 100, AppID: "owned", Title: "lattice[0]: old title"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Exact) != 1 || inventory.Exact[0].Session != "Case" {
		t.Fatalf("inventory=%#v", inventory)
	}
}

func writeMirrorProc(t *testing.T, root string, pid, ppid, start int, argv []string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fields := []string{"S", strconv.Itoa(ppid)}
	for len(fields) < 19 {
		fields = append(fields, "0")
	}
	fields = append(fields, strconv.Itoa(start))
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(strconv.Itoa(pid)+" (proc) "+strings.Join(fields, " ")), 0o600); err != nil {
		t.Fatal(err)
	}
	payload := []byte(strings.Join(argv, "\x00") + "\x00")
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
}
