package mirror

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestProjectionEvidenceAcceptsOnlyCompleteGeneratedForms(t *testing.T) {
	cfg := LaunchConfig{SourceHost: "alias@lattice", SSHCommand: "/usr/bin/ssh", SSHOptions: []string{"-o", "BatchMode=yes"}, LauncherCommand: "kitty", AppID: "owned"}
	for _, tc := range []struct {
		name    string
		session string
		create  bool
		token   bool
	}{
		{name: "attach", session: "Alpha", token: true},
		{name: "create", session: "redeem-0123456789abcdef0123456789abcdef", create: true},
		{name: "leading dash", session: "-hostile'; exec echo NO", token: true},
		{name: "case distinct", session: "alpha"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			window := Window{Order: 1, Title: "ignored", ZellijSession: tc.session, Terminal: &Terminal{CWD: "/tmp/a'b"}}
			launchCfg := cfg
			expectedToken := ""
			if tc.token {
				expectedToken = "0123456789abcdef0123456789abcdef"
				launchCfg.CorrelationToken = expectedToken
			}
			var plan LaunchPlan
			var err error
			if tc.create {
				plan, err = PlanNew(tc.session, launchCfg)
			} else {
				plan, err = PlanLaunch(window, launchCfg)
			}
			if err != nil {
				t.Fatal(err)
			}
			argv := launchSSHArgv(t, plan)
			host, session, token, ok := parseProjectionSSHArgv(argv, cfg.SSHCommand, cfg.SSHOptions)
			if !ok || host != cfg.SourceHost || session != tc.session || token != expectedToken {
				t.Fatalf("parse=(%q,%q,%q,%v) argv=%#v", host, session, token, ok, argv)
			}
			remote := argv[len(argv)-1]
			if strings.HasPrefix(tc.session, "-") && !strings.Contains(remote, "'attach' '--' "+ShellQuote(tc.session)) {
				t.Fatalf("leading-dash plan lacks usable option boundary: %s", remote)
			}
		})
	}
}

func TestProjectionEvidenceRejectsDeceptiveSSHAndShellNearMatches(t *testing.T) {
	cfg := LaunchConfig{SourceHost: "lattice", SSHCommand: "/usr/bin/ssh", SSHOptions: []string{"-o", "BatchMode=yes"}, LauncherCommand: "kitty", AppID: "owned"}
	plan, err := PlanLaunch(Window{ZellijSession: "Alpha"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	base := launchSSHArgv(t, plan)
	mutate := func(fn func([]string) []string) []string { return fn(append([]string(nil), base...)) }
	cases := map[string][]string{
		"wrong executable same basename": mutate(func(v []string) []string { v[0] = "/evil/ssh"; return v }),
		"missing configured option":      append(append([]string(nil), base[:1]...), base[3:]...),
		"extra option":                   append(append([]string(nil), base[:3]...), append([]string{"-F", "evil"}, base[3:]...)...),
		"reordered option":               mutate(func(v []string) []string { v[1], v[2] = v[2], v[1]; return v }),
		"premature boundary":             mutate(func(v []string) []string { v[1] = "--"; return v }),
		"extra operand":                  append(append([]string(nil), base...), "extra"),
		"missing remote":                 append([]string(nil), base[:len(base)-1]...),
		"dynamic remote":                 mutate(func(v []string) []string { v[len(v)-1] = "exec zellij attach Alpha"; return v }),
		"multiple commands":              mutate(func(v []string) []string { v[len(v)-1] += "; exec 'zellij' 'attach' 'B'"; return v }),
		"malformed quote":                mutate(func(v []string) []string { v[len(v)-1] = strings.TrimSuffix(v[len(v)-1], "'"); return v }),
	}
	for name, argv := range cases {
		t.Run(name, func(t *testing.T) {
			if host, session, token, ok := parseProjectionSSHArgv(argv, cfg.SSHCommand, cfg.SSHOptions); ok {
				t.Fatalf("accepted deceptive argv host=%q session=%q token=%q argv=%#v", host, session, token, argv)
			}
		})
	}
}

func TestInspectProjectionsSeparatesAmbiguousUntrackedAndPreUpgrade(t *testing.T) {
	root := t.TempDir()
	writeMirrorProc(t, root, 100, 1, 10, []string{"kitty"})
	cfg := LaunchConfig{SourceHost: "lattice", SSHCommand: "ssh", SSHOptions: []string{"-v"}, LauncherCommand: "kitty", AppID: "owned"}
	planA, _ := PlanLaunch(Window{ZellijSession: "A"}, cfg)
	planB, _ := PlanLaunch(Window{ZellijSession: "B"}, cfg)
	writeMirrorProc(t, root, 101, 100, 11, launchSSHArgv(t, planA))
	writeMirrorProc(t, root, 102, 100, 12, launchSSHArgv(t, planB))
	writeMirrorProc(t, root, 200, 1, 20, []string{"kitty"})
	writeMirrorProc(t, root, 300, 1, 30, []string{"kitty"})
	writeMirrorProc(t, root, 301, 300, 31, launchSSHArgv(t, planA))

	inventory, err := InspectProjections(context.Background(), []OwnedWindow{
		{ID: 1, PID: 100, AppID: "owned", Title: "lattice[0|A]: spoof"},
		{ID: 2, PID: 200, AppID: "owned", Title: "lattice[0|A]: title-only"},
		{ID: 3, PID: 300, AppID: "owned", Title: "lattice[0]: pre-upgrade"},
	}, ProjectionEvidenceConfig{ProcRoot: root, SSHCommand: "ssh", SSHOptions: []string{"-v"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Ambiguous) != 1 || inventory.Ambiguous[0].ID != 1 || len(inventory.Untracked) != 1 || inventory.Untracked[0].ID != 2 {
		t.Fatalf("inventory=%#v", inventory)
	}
	if len(inventory.Exact) != 1 || inventory.Exact[0].Window.ID != 3 || inventory.Exact[0].Session != "A" {
		t.Fatalf("exact=%#v", inventory.Exact)
	}
}

func launchSSHArgv(t *testing.T, plan LaunchPlan) []string {
	t.Helper()
	index := slices.Index(plan.Command.Args, "-e")
	if index < 0 || index+1 >= len(plan.Command.Args) {
		t.Fatalf("launch has no -e SSH argv: %#v", plan.Command.Args)
	}
	return append([]string(nil), plan.Command.Args[index+1:]...)
}

func writeMirrorProc(t *testing.T, root string, pid, ppid, start int, argv []string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	taskDir := filepath.Join(dir, "task", strconv.Itoa(pid))
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
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
	if err := os.WriteFile(filepath.Join(taskDir, "children"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if parent, err := os.OpenFile(filepath.Join(root, strconv.Itoa(ppid), "task", strconv.Itoa(ppid), "children"), os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		if _, err := parent.WriteString(strconv.Itoa(pid) + " "); err != nil {
			_ = parent.Close()
			t.Fatal(err)
		}
		if err := parent.Close(); err != nil {
			t.Fatal(err)
		}
	}
	payload := []byte(strings.Join(argv, "\x00") + "\x00")
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
}
