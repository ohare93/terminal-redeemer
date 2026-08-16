package mirror

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/zellijlive"
)

const generatedTestSession = "redeem-0123456789abcdef0123456789abcdef"

func TestPlanSourceAttachPreservesDirectAndWrappedSnapshotPrefixes(t *testing.T) {
	tests := []struct {
		name     string
		snapshot []string
		want     []string
	}{
		{name: "direct", snapshot: []string{"/nix/store/redeem bin", "mirror", "snapshot"}, want: []string{"/nix/store/redeem bin", "mirror", "attach-local", "--session", generatedTestSession, "--workspace", "agentleman's"}},
		{name: "wrapped", snapshot: []string{"env", "REDEEM_PROFILE=remote", "/nix/store/redeem", "mirror", "snapshot"}, want: []string{"env", "REDEEM_PROFILE=remote", "/nix/store/redeem", "mirror", "attach-local", "--session", generatedTestSession, "--workspace", "agentleman's"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			command, err := PlanSourceAttach(SourceAttachConfig{
				SourceHost: "user@lattice", SSHCommand: "ssh", SSHOptions: []string{"-p", "2222"},
				SnapshotCommand: tc.snapshot, Session: generatedTestSession, Workspace: "agentleman's",
			})
			if err != nil {
				t.Fatal(err)
			}
			if command.Name != "ssh" || !reflect.DeepEqual(command.Args[:4], []string{"-p", "2222", "--", "user@lattice"}) {
				t.Fatalf("SSH boundary: %#v", command)
			}
			if command.Args[4] != QuoteCommand(tc.want) {
				t.Fatalf("remote argv = %q, want %q", command.Args[4], QuoteCommand(tc.want))
			}
		})
	}
	for _, unsupported := range [][]string{{"redeem"}, {"redeem", "mirror", "list"}, {"mirror", "snapshot"}} {
		if _, err := PlanSourceAttach(SourceAttachConfig{SourceHost: "lattice", SSHCommand: "ssh", SnapshotCommand: unsupported, Session: generatedTestSession}); err == nil {
			t.Fatalf("unsupported snapshot command accepted: %#v", unsupported)
		}
	}
}

func TestWorkspaceAndGeneratedSessionValidation(t *testing.T) {
	for _, valid := range []string{"", "agentleman", "workspace 1", "12", "følg"} {
		if err := ValidateWorkspaceReference(valid); err != nil {
			t.Errorf("valid workspace %q: %v", valid, err)
		}
	}
	for _, invalid := range []string{"0", "-1", "--help", " leading", "trailing ", "bad\nname", strings.Repeat("a", 129)} {
		if err := ValidateWorkspaceReference(invalid); err == nil {
			t.Errorf("invalid workspace accepted: %q", invalid)
		}
	}
	if _, err := PlanLocalAttach("existing", "", "kitty"); err == nil {
		t.Fatal("source helper accepted arbitrary existing session")
	}
}

func TestPlanLocalAttachIsDetachedAttachOnly(t *testing.T) {
	plan, err := PlanLocalAttach(generatedTestSession, "agentleman", "kitty")
	if err != nil {
		t.Fatal(err)
	}
	rendered := RenderCommand(plan.Command)
	for _, want := range []string{"'--detach'", "'--class' 'kitty'", "'zellij' 'attach'", "'options' '--on-force-close' 'detach'"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing %s: %s", want, rendered)
		}
	}
	if strings.Contains(rendered, "--create") {
		t.Fatalf("source attach may not create: %s", rendered)
	}
}

type catalogSequence struct {
	mu       sync.Mutex
	statuses []zellijlive.Status
	block    bool
}

func (catalog *catalogSequence) Observe(ctx context.Context) (zellijlive.Catalog, error) {
	if catalog.block {
		<-ctx.Done()
		return zellijlive.Catalog{}, ctx.Err()
	}
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	status := zellijlive.StatusMissing
	if len(catalog.statuses) > 0 {
		status = catalog.statuses[0]
		if len(catalog.statuses) > 1 {
			catalog.statuses = catalog.statuses[1:]
		}
	}
	return zellijlive.Catalog{Sessions: map[string]zellijlive.Session{generatedTestSession: {Name: generatedTestSession, Status: status}}, Names: []string{generatedTestSession}}, nil
}

type concurrentAttachFixture struct {
	mu       sync.Mutex
	window   bool
	launches int
	commands []Command
	moveErr  error
}

func (fixture *concurrentAttachFixture) list(context.Context) ([]OwnedWindow, error) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.window {
		return []OwnedWindow{{ID: 11, PID: 110, AppID: "kitty"}}, nil
	}
	return nil, nil
}

func (fixture *concurrentAttachFixture) attached(_ context.Context, pid int, session string) (bool, error) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.window && pid == 110 && session == generatedTestSession, nil
}

func (fixture *concurrentAttachFixture) Run(_ context.Context, command Command) error {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.commands = append(fixture.commands, command)
	if command.Name == "kitty" {
		fixture.launches++
		fixture.window = true
	}
	if command.Name == "niri" {
		return fixture.moveErr
	}
	return nil
}

func (fixture *concurrentAttachFixture) Output(context.Context, Command) ([]byte, error) {
	return nil, errors.New("unexpected output")
}

func TestAttachLocalConcurrentProcessesLaunchOnceAndMoveByExactID(t *testing.T) {
	plan, _ := PlanLocalAttach(generatedTestSession, "agentleman", "kitty")
	fixture := &concurrentAttachFixture{}
	deps := localAttachDeps{
		runner: fixture, listWindows: fixture.list, attached: fixture.attached,
		catalog:     &catalogSequence{statuses: []zellijlive.Status{zellijlive.StatusActive}},
		niriCommand: "niri", pollInterval: time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stateDir := t.TempDir()
	results := make(chan LocalAttachResult, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			result, err := attachLocal(ctx, plan, stateDir, deps)
			results <- result
			errs <- err
		}()
	}
	alreadyOpen := 0
	for range 2 {
		result, err := <-results, <-errs
		if err != nil {
			t.Fatal(err)
		}
		if result.AlreadyOpen {
			alreadyOpen++
		}
	}
	fixture.mu.Lock()
	launches := fixture.launches
	commands := append([]Command(nil), fixture.commands...)
	fixture.mu.Unlock()
	if launches != 1 || alreadyOpen != 1 {
		t.Fatalf("launches=%d alreadyOpen=%d", launches, alreadyOpen)
	}
	wantMove := Command{Name: "niri", Args: []string{"msg", "action", "move-window-to-workspace", "--window-id", "11", "--focus", "false", "agentleman"}}
	moves := make([]Command, 0, 1)
	for _, command := range commands {
		if command.Name == "niri" {
			moves = append(moves, command)
		}
	}
	if !reflect.DeepEqual(moves, []Command{wantMove}) {
		t.Fatalf("move commands = %#v, want %#v", moves, []Command{wantMove})
	}
}

func TestAttachLocalPlacementFailureIsNonFatal(t *testing.T) {
	plan, _ := PlanLocalAttach(generatedTestSession, "agentleman", "kitty")
	fixture := &concurrentAttachFixture{moveErr: errors.New("workspace unavailable")}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := attachLocal(ctx, plan, t.TempDir(), localAttachDeps{
		runner: fixture, listWindows: fixture.list, attached: fixture.attached,
		catalog:     &catalogSequence{statuses: []zellijlive.Status{zellijlive.StatusActive}},
		niriCommand: "niri", pollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("placement failure became fatal: %v", err)
	}
	if result.WindowID != 11 || result.PlacementError == nil || !strings.Contains(result.PlacementError.Error(), "workspace unavailable") {
		t.Fatalf("result = %#v", result)
	}
}

func TestAttachLocalRequiresVerifiedActiveCatalogAndRealDeadline(t *testing.T) {
	plan, _ := PlanLocalAttach(generatedTestSession, "", "kitty")
	runner := &recordingRunner{}
	deps := localAttachDeps{
		runner: runner, listWindows: func(context.Context) ([]OwnedWindow, error) { return nil, nil },
		attached: func(context.Context, int, string) (bool, error) { return false, nil },
		catalog:  &catalogSequence{statuses: []zellijlive.Status{zellijlive.StatusMissing}}, pollInterval: time.Millisecond,
	}
	if _, err := attachLocal(context.Background(), plan, t.TempDir(), deps); err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("missing deadline accepted: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := attachLocal(ctx, plan, t.TempDir(), deps); err == nil || !strings.Contains(err.Error(), "did not become active") {
		t.Fatalf("name-only catalog accepted: %v", err)
	}
	if len(runner.runCalls) != 0 {
		t.Fatalf("inactive session launched Kitty: %#v", runner.runCalls)
	}

	blocking := deps
	blocking.catalog = &catalogSequence{block: true}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := attachLocal(ctx, plan, t.TempDir(), blocking); err == nil || time.Since(started) > time.Second {
		t.Fatalf("blocking catalog was not bounded: %v", err)
	}
}

type blockingCommandRunner struct{}

func (blockingCommandRunner) Output(ctx context.Context, _ Command) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (blockingCommandRunner) Run(ctx context.Context, _ Command) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestExecRunnerBoundsPipeHoldingDescendants(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := (ExecRunner{}).Output(ctx, Command{Name: "sh", Args: []string{"-c", "sleep 5 & wait"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("mirror command exceeded wall-clock bound: %s", elapsed)
	}
}

func TestAttachLocalBoundsBlockingWindowAndKittyOperations(t *testing.T) {
	plan, _ := PlanLocalAttach(generatedTestSession, "", "kitty")
	active := &catalogSequence{statuses: []zellijlive.Status{zellijlive.StatusActive}}
	t.Run("window observation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		_, err := attachLocal(ctx, plan, t.TempDir(), localAttachDeps{
			runner:      &recordingRunner{},
			listWindows: func(ctx context.Context) ([]OwnedWindow, error) { <-ctx.Done(); return nil, ctx.Err() },
			attached:    func(context.Context, int, string) (bool, error) { return false, nil },
			catalog:     active, pollInterval: time.Millisecond,
		})
		if err == nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocking window observation not bounded: %v", err)
		}
	})
	t.Run("Kitty launch", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		_, err := attachLocal(ctx, plan, t.TempDir(), localAttachDeps{
			runner:      blockingCommandRunner{},
			listWindows: func(context.Context) ([]OwnedWindow, error) { return nil, nil },
			attached:    func(context.Context, int, string) (bool, error) { return false, nil },
			catalog:     active, pollInterval: time.Millisecond,
		})
		if err == nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocking Kitty launch not bounded: %v", err)
		}
	})
}

func TestLocalProcAttachmentProbeRequiresExactStableDescendantArgv(t *testing.T) {
	root := t.TempDir()
	writeProcFixture(t, root, 100, 1, 10, []string{"kitty", "--title", generatedTestSession})
	writeProcFixture(t, root, 101, 100, 11, []string{"zellij", "attach", generatedTestSession, "options", "--on-force-close", "detach"})
	probe := localProcAttachmentProbe{procRoot: root}
	attached, err := probe.attached(context.Background(), 100, generatedTestSession)
	if err != nil || !attached {
		t.Fatalf("attached=%v err=%v", attached, err)
	}
}

func writeProcFixture(t *testing.T, root string, pid int, ppid int, start int, argv []string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeProcStat(t, root, pid, ppid, start)
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeProcStat(t *testing.T, root string, pid int, ppid int, start int) {
	t.Helper()
	fields := []string{"S", strconv.Itoa(ppid)}
	for len(fields) < 19 {
		fields = append(fields, "0")
	}
	fields = append(fields, strconv.Itoa(start))
	payload := strconv.Itoa(pid) + " (proc name) " + strings.Join(fields, " ")
	if err := os.WriteFile(filepath.Join(root, strconv.Itoa(pid), "stat"), []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGraphicalEnvironmentUsesCompleteManagerTupleAuthoritatively(t *testing.T) {
	runner := &recordingRunner{outputs: []outputResult{{data: []byte("NIRI_SOCKET=/run/new.sock\nWAYLAND_DISPLAY=wayland-new\nXDG_RUNTIME_DIR=/run/user/1000\nDISPLAY=:99\n")}}}
	env := []string{"PATH=/bin", "NIRI_SOCKET=/run/old.sock", "WAYLAND_DISPLAY=wayland-old", "XDG_RUNTIME_DIR=/tmp/old"}
	got, err := graphicalEnvironment(context.Background(), env, runner)
	if err != nil {
		t.Fatal(err)
	}
	values := environmentValues(got)
	if values["NIRI_SOCKET"] != "/run/new.sock" || values["WAYLAND_DISPLAY"] != "wayland-new" || values["XDG_RUNTIME_DIR"] != "/run/user/1000" || values["DISPLAY"] != "" {
		t.Fatalf("environment=%#v", values)
	}

	runner = &recordingRunner{outputs: []outputResult{{data: []byte("NIRI_SOCKET=/run/only.sock\n")}}}
	if _, err := graphicalEnvironment(context.Background(), []string{"PATH=/bin"}, runner); err == nil {
		t.Fatal("incomplete manager environment accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := graphicalEnvironment(ctx, nil, blockingCommandRunner{}); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocking user-manager query not bounded: %v", err)
	}
}
