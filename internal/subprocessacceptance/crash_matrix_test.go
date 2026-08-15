//go:build linux

package subprocessacceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/sliceprotocol"
	"github.com/jmo/terminal-redeemer/internal/slicerpc"
	"github.com/jmo/terminal-redeemer/internal/sourceinventory"
)

var routedCrashStages = []string{
	"pending", "session_starting", "session_created", "socket_planned",
	"kitty_prepared", "kitty_starting", "placed", "proof_committed", "committed",
}

func crashRequest(t *testing.T, verb slicerpc.Verb, token string) []byte {
	t.Helper()
	payload, err := json.Marshal(slicerpc.TokenPayload{Token: token, SessionName: slicerpc.StableSessionName(token), WorkspaceName: "Work"})
	if verb == slicerpc.VerbLaunch {
		payload, err = json.Marshal(slicerpc.LaunchPayload{Token: token, SessionName: slicerpc.StableSessionName(token), WorkspaceName: "Work"})
	}
	if err != nil {
		t.Fatal(err)
	}
	request := slicerpc.Request{SchemaVersion: slicerpc.SchemaVersion, AcceptSchemaVersions: []uint32{1}, RequestID: "crash-" + token, Verb: verb, Payload: payload}
	out, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

func waitTokenStage(t *testing.T, state, token, stage string) slicerpc.TokenRecord {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		store, err := slicerpc.NewTokenStore(state)
		if err == nil {
			if record, readErr := store.Read(token); readErr == nil && record.Stage == stage {
				return record
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for token %s stage %s", token, stage)
	return slicerpc.TokenRecord{}
}

func readOnlyTokenRecord(t *testing.T, state, token string) slicerpc.TokenRecord {
	t.Helper()
	store, err := slicerpc.NewTokenStore(state)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Read(token)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func runPackagedCrashRPCRaw(t *testing.T, transport, redeem string, env []string, request []byte) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	args := []string{"-T", "-o", "ServerAliveInterval=1", "-o", "ServerAliveCountMax=1", "--", "host.test", redeem, "slice", "rpc"}
	cmd := exec.CommandContext(ctx, transport, args...)
	cmd.Env = append([]string(nil), env...)
	cmd.Stdin = bytes.NewReader(request)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	payload, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return payload, fmt.Errorf("packaged RPC deadline: %w", ctx.Err())
	}
	return payload, err
}

func runPackagedCrashRPC(t *testing.T, transport, redeem string, env []string, request []byte) []byte {
	t.Helper()
	payload, err := runPackagedCrashRPCRaw(t, transport, redeem, env, request)
	if err != nil {
		t.Fatalf("packaged shell-inert RPC failed: %v output=%s", err, payload)
	}
	return payload
}

func rpcResponse(t *testing.T, payload []byte) slicerpc.Response {
	t.Helper()
	var response slicerpc.Response
	if err := decodeFirstJSONLine(payload, &response); err != nil {
		t.Fatalf("decode RPC response: %v: %s", err, payload)
	}
	return response
}

func replayUntilStatus(t *testing.T, transport, redeem string, env []string, request []byte, want slicerpc.OutcomeStatus) slicerpc.Response {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	var response slicerpc.Response
	for {
		response = rpcResponse(t, runPackagedCrashRPC(t, transport, redeem, env, request))
		if response.Outcome.Status == want {
			return response
		}
		if response.Outcome.Status != slicerpc.StatusPending || time.Now().After(deadline) {
			t.Fatalf("packaged replay did not reach %s: %+v", want, response)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func startCrashRPC(t *testing.T, helper string, env []string, input []byte) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	cmd := exec.Command(helper)
	cmd.Env = append(append([]string(nil), env...), "TERMINAL_REDEEMER_CRASH_RPC_HELPER=1")
	cmd.Stdin = bytes.NewReader(input)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	buffer := &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = buffer, buffer
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd, buffer
}

func waitCrashGate(t *testing.T, cmd *exec.Cmd, output *bytes.Buffer, ledger, token, stage string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if countInv(readInvocations(t, ledger), "stage-gated", func(v invocation) bool { return v.Token == token && v.Verb == stage }) == 1 {
			return
		}
		if processExited(cmd.Process.Pid) {
			err := cmd.Wait()
			t.Fatalf("crash RPC exited before %s gate: %v %s ledger=%+v", stage, err, output.Bytes(), readInvocations(t, ledger))
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s gate: %s", stage, output.Bytes())
}

func killGatedRPC(t *testing.T, cmd *exec.Cmd, owner string) processIdentity {
	t.Helper()
	proc, err := pinStartedOwnedProcess(cmd.Process.Pid, processIdentity{}, owner)
	if err != nil {
		t.Fatalf("pin responsible crash RPC: %v", err)
	}
	defer proc.close()
	identity := proc.identity
	if err := proc.signal(syscall.SIGKILL); err != nil {
		t.Fatalf("pidfd kill responsible crash RPC: %v", err)
	}
	_ = cmd.Wait()
	if exited, err := proc.wait(2 * time.Second); err != nil || !exited {
		t.Fatalf("responsible RPC did not exit: exited=%t err=%v", exited, err)
	}
	return identity
}

func listExactSessionCount(t *testing.T, zellij string, env []string, session string) int {
	t.Helper()
	cmd := exec.Command(zellij, "list-sessions", "--short")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil && !bytes.Contains(out, []byte("No active zellij sessions found")) {
		t.Fatalf("list sessions: %v %s", err, out)
	}
	count := 0
	for _, field := range strings.Fields(string(out)) {
		if field == session {
			count++
		}
	}
	return count
}

func waitExactSessionCount(t *testing.T, zellij string, env []string, session string, want int) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		count := listExactSessionCount(t, zellij, env, session)
		if count == want || count > 1 || time.Now().After(deadline) {
			return count
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type crashCardinalityBaseline struct {
	tokenFiles, actions, fallback, projections, handoffs, sentinelActions int
}

type crashCardinalityExpected struct {
	status                               slicerpc.TokenStatus
	stage                                string
	sessions, kitty, placements, sources int
	prepared                             bool
}

func countGlobs(patterns ...string) int {
	count := 0
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		count += len(matches)
	}
	return count
}

func countExactSocketPaths(t *testing.T, root, session string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.Name() == session && entry.Type()&os.ModeSocket != 0 {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func countWindowPlacements(lines []string, windowID uint64) int {
	if windowID == 0 {
		return 0
	}
	needle := fmt.Sprintf(`"window_id":%d`, windowID)
	count := 0
	for _, line := range lines {
		if strings.Contains(line, `"MoveWindowToWorkspace"`) && strings.Contains(line, needle) {
			count++
		}
	}
	return count
}

func countSentinelActions(lines []string) int {
	count := 0
	for _, line := range lines {
		if strings.Contains(line, `"id":90`) || strings.Contains(line, `"window_id":90`) {
			count++
		}
	}
	return count
}

func captureCrashCardinality(t *testing.T, root, state string, niri *niriServer, ledger string) crashCardinalityBaseline {
	t.Helper()
	all := readInvocations(t, ledger)
	tokens, _ := filepath.Glob(filepath.Join(state, "slice", "rpc-tokens", "*.json"))
	return crashCardinalityBaseline{
		tokenFiles: len(tokens), actions: len(niri.actionLines()),
		fallback:        countInv(all, "kitty", func(v invocation) bool { return len(v.Argv) == 1 }),
		projections:     countInv(all, "ssh", func(v invocation) bool { return v.Verb == "attach" }),
		handoffs:        countGlobs(filepath.Join(root, "l", "state", "slice", "launch", "intents", "*.json"), filepath.Join(root, "l", "state", "slice", "controller", "current.json")),
		sentinelActions: countSentinelActions(niri.actionLines()),
	}
}

func assertCrashCardinality(t *testing.T, label, root, redeem, zellij string, host node, hostEnv []string, niri *niriServer, ledger string, baseline crashCardinalityBaseline, token string, want crashCardinalityExpected) slicerpc.TokenRecord {
	t.Helper()
	record := readOnlyTokenRecord(t, host.state, token)
	if record.Status != want.status || record.Stage != want.stage {
		t.Fatalf("%s terminal record=%+v want status=%s stage=%s", label, record, want.status, want.stage)
	}
	if got := waitExactSessionCount(t, zellij, hostEnv, record.SessionName, want.sessions); got != want.sessions {
		t.Fatalf("%s exact session cardinality=%d want=%d", label, got, want.sessions)
	}
	all := readInvocations(t, ledger)
	kitty := countInv(all, "kitty", func(v invocation) bool { return contains(v.Argv, record.SessionName) })
	if kitty != want.kitty {
		t.Fatalf("%s total host Kitty starts=%d want=%d", label, kitty, want.kitty)
	}
	placements := countWindowPlacements(niri.actionLines(), record.NiriWindowID)
	if placements != want.placements {
		t.Fatalf("%s total exact-window placements=%d want=%d window=%d actions=%v", label, placements, want.placements, record.NiriWindowID, niri.actionLines()[baseline.actions:])
	}
	prepared := record.PreparedSocketPath != "" && markerPresent(record.PreparedSocketPath)
	if prepared != want.prepared {
		t.Fatalf("%s prepared namespace present=%t want=%t path=%q", label, prepared, want.prepared, record.PreparedSocketPath)
	}
	socketsWant := want.sessions
	if want.prepared {
		socketsWant++
	}
	if sockets := countExactSocketPaths(t, host.runtime, record.SessionName); sockets != socketsWant {
		t.Fatalf("%s exact socket paths=%d want=%d", label, sockets, socketsWant)
	}
	tokens, _ := filepath.Glob(filepath.Join(host.state, "slice", "rpc-tokens", "*.json"))
	if len(tokens) != baseline.tokenFiles+1 {
		t.Fatalf("%s token files=%d want=%d", label, len(tokens), baseline.tokenFiles+1)
	}
	snapshot := runOK(t, redeem, host.config, hostEnv, "slice", "inventory", "snapshot", "--state-dir", host.state, "--accept-schema-version", "2")
	var envelope sliceprotocol.Envelope
	if err := json.Unmarshal(snapshot, &envelope); err != nil || envelope.Observation.Quality != sliceprotocol.QualityComplete || envelope.Authoritative == nil {
		t.Fatalf("%s complete authority unavailable: err=%v snapshot=%s", label, err, snapshot)
	}
	sources := 0
	for _, source := range envelope.Authoritative.Sources {
		if source.Session.Name != record.SessionName {
			continue
		}
		sources++
		expectedID, idErr := sourceinventory.SourceID(record.TransactionEpoch, record.NiriWindowID)
		if idErr != nil || source.SourceID != expectedID || source.RuntimeWindowID != record.NiriWindowID || source.Workspace.Name != record.WorkspaceName || envelope.Authoritative.SourceEpoch != record.TransactionEpoch {
			t.Fatalf("%s source tuple mismatch: source=%+v record=%+v authority_epoch=%s err=%v", label, source, record, envelope.Authoritative.SourceEpoch, idErr)
		}
	}
	if sources != want.sources {
		t.Fatalf("%s exact source identities=%d want=%d", label, sources, want.sources)
	}
	if want.sources == 1 && (record.SourceID == "" || record.SourceEpoch != record.TransactionEpoch) {
		t.Fatalf("%s committed journal source identity incomplete: %+v", label, record)
	}
	fallback := countInv(all, "kitty", func(v invocation) bool { return len(v.Argv) == 1 })
	projections := countInv(all, "ssh", func(v invocation) bool { return v.Verb == "attach" })
	handoffs := countGlobs(filepath.Join(root, "l", "state", "slice", "launch", "intents", "*.json"), filepath.Join(root, "l", "state", "slice", "controller", "current.json"))
	if fallback != baseline.fallback || projections != baseline.projections || handoffs != baseline.handoffs || markerPresent(filepath.Join(root, "l")) {
		t.Fatalf("%s forbidden totals changed: fallback=%d/%d projections=%d/%d handoffs=%d/%d", label, fallback, baseline.fallback, projections, baseline.projections, handoffs, baseline.handoffs)
	}
	if !niri.sentinelPresent() || countSentinelActions(niri.actionLines()) != baseline.sentinelActions {
		t.Fatalf("%s sentinel was lost or acted upon", label)
	}
	t.Logf("case=%s total token=1 session=%d sockets=%d kitty=%d placement=%d source=%d handoff=0 projection=0 fallback=0 sentinel_actions=0 prepared=%t terminal=%s/%s", label, want.sessions, socketsWant, kitty, placements, sources, want.prepared, record.Status, record.Stage)
	return record
}

// TestRoutedLaunchPackagedProcessCrashMatrix is serial because every case uses
// real Zellij servers and long-lived Kitty/helper process trees. Immutable
// binaries, the Niri server, inventory authority, and the seed session are
// shared; each stage has a distinct token and durable journal.
func TestRoutedLaunchPackagedProcessCrashMatrix(t *testing.T) {
	redeem, zellij := requiredBinary(t, "REDEEM_BIN"), requiredBinary(t, "ZELLIJ_BIN")
	startedAt := time.Now()
	root, err := os.MkdirTemp("/tmp", "trc-")
	if err != nil {
		t.Fatal(err)
	}
	ledger, faults := filepath.Join(root, "ledger.jsonl"), filepath.Join(root, "faults")
	mustMkdir(t, faults)
	retain := os.Getenv("TERMINAL_REDEEMER_RETAIN_CRASH_MATRIX") == "1"
	t.Cleanup(func() {
		cleanupOwnedFixture(t, root, ledger, faults)
		if retain {
			t.Logf("retained crash matrix state by explicit local opt-in: %s", root)
			return
		}
		_ = os.RemoveAll(root)
	})
	if len(root) > 35 {
		t.Fatalf("temporary root too long for sockets: %s", root)
	}
	bin := filepath.Join(root, "b")
	mustMkdir(t, bin)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if helper := os.Getenv("HARNESS_HELPER_BIN"); helper != "" {
		self, err = filepath.EvalSymlinks(helper)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"niri", "ssh", "kitty", "systemctl", "crash-rpc"} {
		copyExecutable(t, self, filepath.Join(bin, name))
	}
	host := newNode(t, root, "h")
	niri, err := newNiriServer(filepath.Join(host.runtime, "n"), os.Getpid(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer niri.close()
	hostEnv := nodeEnv(host, niri.path, filepath.Join(host.runtime, "z"))
	writeConfig(t, host.config, host.state, redeem, zellij, filepath.Join(bin, "kitty"), filepath.Join(bin, "niri"), filepath.Join(bin, "systemctl"), "", false)
	topo := topology{Redeem: redeem, Zellij: zellij, HostConfig: host.config, HostState: host.state, HostEnv: hostEnv, Ledger: ledger, FaultDir: faults, OwnerID: root}
	writeJSON(t, filepath.Join(bin, topologyName), topo)

	seed := exec.Command(zellij, "attach", "--create-background", "crash-matrix-seed")
	seed.Env, seed.SysProcAttr = hostEnv, &syscall.SysProcAttr{Setpgid: true}
	if out, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("create seed session: %v %s", err, out)
	}
	defer cleanupZellijProcesses(t, root, host.runtime)
	waitZellijControlReady(t, zellij, hostEnv, "crash-matrix-seed")
	runOK(t, redeem, host.config, hostEnv, "slice", "inventory", "init", "--state-dir", host.state)
	var snapshot []byte
	var authority struct {
		SourceHostID  string `json:"source_host_id"`
		Authoritative *struct {
			SourceEpoch string `json:"source_epoch"`
		} `json:"authoritative"`
	}
	inventoryDeadline := time.Now().Add(15 * time.Second)
	for authority.Authoritative == nil {
		snapshot = runOK(t, redeem, host.config, hostEnv, "slice", "inventory", "snapshot", "--state-dir", host.state, "--niri-socket", niri.path, "--niri-command", filepath.Join(bin, "niri"), "--zellij-command", zellij, "--zellij-socket-dir", filepath.Join(host.runtime, "z"), "--zellij-cache-home", host.cache, "--timeout", "10s", "--accept-schema-version", "2")
		authority = struct {
			SourceHostID  string `json:"source_host_id"`
			Authoritative *struct {
				SourceEpoch string `json:"source_epoch"`
			} `json:"authoritative"`
		}{}
		if err := json.Unmarshal(snapshot, &authority); err != nil {
			t.Fatalf("decode inventory authority: %v %s", err, snapshot)
		}
		if authority.Authoritative == nil {
			if time.Now().After(inventoryDeadline) {
				t.Fatalf("inventory authority remained degraded: %s", snapshot)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	// Private fingerprint is deliberately not public in the envelope; read the
	// enrolled inventory state used by the packaged server.
	statePayload, err := os.ReadFile(filepath.Join(host.state, "slice", "source-inventory", "current.json"))
	if err != nil {
		matches, _ := filepath.Glob(filepath.Join(host.state, "slice", "source-inventory", "*.json"))
		for _, match := range matches {
			if payload, readErr := os.ReadFile(match); readErr == nil && bytes.Contains(payload, []byte("private_fingerprint")) {
				statePayload = payload
				break
			}
		}
	}
	var private struct {
		PrivateFingerprint string `json:"private_fingerprint"`
	}
	if json.Unmarshal(statePayload, &private) != nil || private.PrivateFingerprint == "" {
		t.Fatalf("inventory private fingerprint unavailable: %s", statePayload)
	}
	topo.SourceHostID, topo.SourceEpoch, topo.SourceFingerprint = authority.SourceHostID, authority.Authoritative.SourceEpoch, private.PrivateFingerprint
	writeJSON(t, filepath.Join(bin, topologyName), topo)

	unresolved := map[string]bool{"session_starting": true, "kitty_starting": true}
	outcomes := map[string]string{}
	for index, stage := range routedCrashStages {
		stage := stage
		if only := os.Getenv("TERMINAL_REDEEMER_CRASH_MATRIX_STAGE"); only != "" && only != stage {
			continue
		}
		t.Run(stage, func(t *testing.T) {
			token := fmt.Sprintf("matrix-%02d-%s", index, stage)
			baseline := captureCrashCardinality(t, root, host.state, niri, ledger)
			mustWrite(t, filepath.Join(faults, "gate-stage"), []byte(stage))
			cmd, buffer := startCrashRPC(t, filepath.Join(bin, "crash-rpc"), hostEnv, crashRequest(t, slicerpc.VerbLaunch, token))
			waitCrashGate(t, cmd, buffer, ledger, token, stage)
			identity := killGatedRPC(t, cmd, root)
			if buffer.Len() != 0 {
				t.Logf("pre-crash process output: %s", buffer.Bytes())
			}
			_ = os.Remove(filepath.Join(faults, "gate-stage"))
			record := readOnlyTokenRecord(t, host.state, token)
			if record.Stage != stage {
				t.Fatalf("durable crash evidence stage=%q want=%q record=%+v", record.Stage, stage, record)
			}
			if stage == "committed" && record.Status != slicerpc.TokenLaunched || stage != "committed" && record.Status != slicerpc.TokenPending {
				t.Fatalf("unexpected status at crash boundary: %+v", record)
			}
			preReplay := readInvocations(t, ledger)
			actionsBeforeReplay := len(niri.actionLines())
			kittyBeforeReplay := countInv(preReplay, "kitty", func(v invocation) bool { return contains(v.Argv, record.SessionName) })
			if stage == "placed" || stage == "proof_committed" || stage == "committed" {
				if got := countWindowPlacements(niri.actionLines(), record.NiriWindowID); got != 1 {
					t.Fatalf("pre-crash exact placement cardinality=%d want=1 at durable %s", got, stage)
				}
			}
			replay := runPackagedCrashRPC(t, filepath.Join(bin, "ssh"), redeem, hostEnv, crashRequest(t, slicerpc.VerbTokenReplay, token))
			response := rpcResponse(t, replay)
			final := readOnlyTokenRecord(t, host.state, token)
			if unresolved[stage] {
				if response.Outcome.Status != slicerpc.StatusPending || final.Stage != stage || final.Status != slicerpc.TokenPending {
					t.Fatalf("ambiguous boundary was guessed away: response=%+v record=%+v", response, final)
				}
				outcomes[stage] = "inspectable_pending"
			} else {
				if response.Outcome.Status != slicerpc.StatusOK || final.Stage != "committed" || final.Status != slicerpc.TokenLaunched || final.SourceID == "" || final.SourceEpoch != topo.SourceEpoch {
					t.Fatalf("replay did not commit exact identity: response=%+v record=%+v", response, final)
				}
				outcomes[stage] = "replayed_committed"
				if stage == "committed" {
					outcomes[stage] = "response_loss_effect_free"
				}
			}
			sessionWant, kittyWant, placementWant, sourceWant, preparedWant := 1, 1, 1, 1, true
			if stage == "session_starting" {
				sessionWant, kittyWant, placementWant, sourceWant, preparedWant = 0, 0, 0, 0, false
			} else if stage == "kitty_starting" {
				kittyWant, placementWant, sourceWant = 0, 0, 0
			}
			after := readInvocations(t, ledger)
			expectedPreparedPath := final.PreparedSocketPath
			for _, entry := range after {
				if entry.Kind == "kitty" && contains(entry.Argv, final.SessionName) && (!contains(entry.Argv, "host-attach") || !contains(entry.Argv, expectedPreparedPath)) {
					t.Fatalf("ordinary/prefix attachment escaped exact packaged argv: %+v", entry)
				}
			}
			checked := assertCrashCardinality(t, stage, root, redeem, zellij, host, hostEnv, niri, ledger, baseline, token, crashCardinalityExpected{status: final.Status, stage: final.Stage, sessions: sessionWant, kitty: kittyWant, placements: placementWant, sources: sourceWant, prepared: preparedWant})
			if stage == "committed" && (countInv(after, "kitty", func(v invocation) bool { return contains(v.Argv, checked.SessionName) }) != kittyBeforeReplay || len(niri.actionLines()) != actionsBeforeReplay) {
				t.Fatalf("committed packaged replay was effectful: kitty %d->%d actions %d->%d", kittyBeforeReplay, kittyWant, actionsBeforeReplay, len(niri.actionLines()))
			}
			if got := countInv(after, "ssh", func(v invocation) bool { return v.Verb == "token_replay" && v.Token == token }); got != 2 {
				t.Fatalf("stage %s packaged transport replay ledger edges=%d want=2 (start+finish)", stage, got)
			}
			t.Logf("stage=%s pid=%d/%d durable=%s outcome=%s packaged_replay=true production_proof=%t", stage, identity.PID, identity.StartTime, record.Stage, outcomes[stage], !unresolved[stage])
		})
	}

	if os.Getenv("TERMINAL_REDEEMER_CRASH_MATRIX_STAGE") == "" {
		// Cancellation is isolated from child delay: terminate the exact owner at
		// the post-fsync kitty_starting gate before Kitty exec, then require the
		// packaged replay to preserve inspectable ambiguity without guessing.
		t.Run("cancellation", func(t *testing.T) {
			token := "matrix-cancellation"
			baseline := captureCrashCardinality(t, root, host.state, niri, ledger)
			mustWrite(t, filepath.Join(faults, "gate-stage"), []byte("kitty_starting"))
			cmd, output := startCrashRPC(t, filepath.Join(bin, "crash-rpc"), hostEnv, crashRequest(t, slicerpc.VerbLaunch, token))
			waitCrashGate(t, cmd, output, ledger, token, "kitty_starting")
			proc, err := pinStartedOwnedProcess(cmd.Process.Pid, processIdentity{}, root)
			if err != nil {
				t.Fatal(err)
			}
			if err := proc.signal(syscall.SIGTERM); err != nil {
				proc.close()
				t.Fatal(err)
			}
			_ = cmd.Wait()
			proc.close()
			_ = os.Remove(filepath.Join(faults, "gate-stage"))
			response := rpcResponse(t, runPackagedCrashRPC(t, filepath.Join(bin, "ssh"), redeem, hostEnv, crashRequest(t, slicerpc.VerbTokenReplay, token)))
			if response.Outcome.Status != slicerpc.StatusPending {
				t.Fatalf("cancellation was guessed away: %+v", response)
			}
			assertCrashCardinality(t, "cancellation", root, redeem, zellij, host, hostEnv, niri, ledger, baseline, token, crashCardinalityExpected{status: slicerpc.TokenPending, stage: "kitty_starting", sessions: 1, kitty: 0, placements: 0, sources: 0, prepared: true})
		})

		// A child delayed after Kitty exec survives a hard RPC crash. Once the
		// exact child connects, packaged replay must adopt it without a restart.
		t.Run("delayed_child_connect", func(t *testing.T) {
			token := "matrix-delayed-child"
			baseline := captureCrashCardinality(t, root, host.state, niri, ledger)
			mustWrite(t, filepath.Join(faults, "gate-next-kitty-child"), []byte("armed"))
			cmd, output := startCrashRPC(t, filepath.Join(bin, "crash-rpc"), hostEnv, crashRequest(t, slicerpc.VerbLaunch, token))
			waitInvocations(t, ledger, 1, "kitty-child-gated", func(v invocation) bool { return contains(v.Argv, slicerpc.StableSessionName(token)) })
			record := waitTokenStage(t, host.state, token, "kitty_starting")
			killGatedRPC(t, cmd, root)
			mustWrite(t, filepath.Join(faults, "release-kitty-child"), []byte("release"))
			connectDeadline := time.Now().Add(10 * time.Second)
			for !processTreeContains(record.KittyPID, "zellij") {
				// The journal intentionally has no PID at kitty_starting; recover
				// the one exact gated Kitty PID from the owned ledger.
				for _, entry := range readInvocations(t, ledger) {
					if entry.Kind == "kitty-child-gated" && contains(entry.Argv, record.SessionName) {
						record.KittyPID = entry.PID
					}
				}
				if time.Now().After(connectDeadline) {
					t.Fatalf("delayed child never connected for %s", record.SessionName)
				}
				time.Sleep(5 * time.Millisecond)
			}
			response := replayUntilStatus(t, filepath.Join(bin, "ssh"), redeem, hostEnv, crashRequest(t, slicerpc.VerbTokenReplay, token), slicerpc.StatusOK)
			if response.Outcome.Status != slicerpc.StatusOK {
				t.Fatalf("delayed child was not adopted: output=%s response=%+v", output.Bytes(), response)
			}
			assertCrashCardinality(t, "delayed_child_connect", root, redeem, zellij, host, hostEnv, niri, ledger, baseline, token, crashCardinalityExpected{status: slicerpc.TokenLaunched, stage: "committed", sessions: 1, kitty: 1, placements: 1, sources: 1, prepared: true})
		})

		// Distinct ambiguous post-start crash: Kitty and its exact Niri window
		// exist, but kitty_started has not yet been journaled.
		t.Run("ambiguous_post_start", func(t *testing.T) {
			token := "matrix-ambiguous-post-start"
			baseline := captureCrashCardinality(t, root, host.state, niri, ledger)
			mustWrite(t, filepath.Join(faults, "gate-kitty-after-start"), []byte("armed"))
			cmd, _ := startCrashRPC(t, filepath.Join(bin, "crash-rpc"), hostEnv, crashRequest(t, slicerpc.VerbLaunch, token))
			waitInvocations(t, ledger, 1, "kitty-start-gated", func(v invocation) bool { return v.Token == token })
			record := readOnlyTokenRecord(t, host.state, token)
			if record.Stage != "kitty_starting" {
				t.Fatalf("post-start crash stage=%s", record.Stage)
			}
			killGatedRPC(t, cmd, root)
			response := replayUntilStatus(t, filepath.Join(bin, "ssh"), redeem, hostEnv, crashRequest(t, slicerpc.VerbTokenReplay, token), slicerpc.StatusOK)
			if response.Outcome.Status != slicerpc.StatusOK {
				t.Fatalf("ambiguous post-start replay=%+v", response)
			}
			assertCrashCardinality(t, "ambiguous_post_start", root, redeem, zellij, host, hostEnv, niri, ledger, baseline, token, crashCardinalityExpected{status: slicerpc.TokenLaunched, stage: "committed", sessions: 1, kitty: 1, placements: 1, sources: 1, prepared: true})
		})

		// Delayed inventory is isolated from crash ambiguity: keep the live Kitty
		// registration unpublished, release it, let the initial transaction finish,
		// then prove packaged replay is effect-free.
		t.Run("delayed_inventory", func(t *testing.T) {
			token := "matrix-delayed-inventory"
			baseline := captureCrashCardinality(t, root, host.state, niri, ledger)
			niri.holdRegistrations(true)
			cmd, output := startCrashRPC(t, filepath.Join(bin, "crash-rpc"), hostEnv, crashRequest(t, slicerpc.VerbLaunch, token))
			record := waitTokenStage(t, host.state, token, "kitty_starting")
			waitInvocations(t, ledger, 1, "kitty", func(v invocation) bool { return contains(v.Argv, record.SessionName) })
			kittyPID := 0
			deadline := time.Now().Add(10 * time.Second)
			for kittyPID == 0 || !processTreeContains(kittyPID, "zellij") {
				for _, entry := range readInvocations(t, ledger) {
					if entry.Kind == "kitty" && contains(entry.Argv, record.SessionName) {
						kittyPID = entry.PID
					}
				}
				if time.Now().After(deadline) {
					t.Fatalf("delayed-inventory Kitty child did not become attachable: pid=%d", kittyPID)
				}
				time.Sleep(5 * time.Millisecond)
			}
			niri.holdRegistrations(false)
			if err := cmd.Wait(); err != nil {
				t.Fatalf("delayed inventory initial RPC: %v %s", err, output.Bytes())
			}
			if response := rpcResponse(t, output.Bytes()); response.Outcome.Status != slicerpc.StatusOK {
				t.Fatalf("delayed inventory did not resolve the existing start: %+v", response)
			}
			if response := rpcResponse(t, runPackagedCrashRPC(t, filepath.Join(bin, "ssh"), redeem, hostEnv, crashRequest(t, slicerpc.VerbTokenReplay, token))); response.Outcome.Status != slicerpc.StatusOK {
				t.Fatalf("delayed inventory replay=%+v", response)
			}
			assertCrashCardinality(t, "delayed_inventory", root, redeem, zellij, host, hostEnv, niri, ledger, baseline, token, crashCardinalityExpected{status: slicerpc.TokenLaunched, stage: "committed", sessions: 1, kitty: 1, placements: 1, sources: 1, prepared: true})
		})

		// Distinct definite pre-start failure: the harness transaction returns
		// Started=false before exec, so only marker-owned prepared state is cleaned.
		t.Run("definite_pre_start_failure", func(t *testing.T) {
			token := "matrix-definite-prestart"
			baseline := captureCrashCardinality(t, root, host.state, niri, ledger)
			mustWrite(t, filepath.Join(faults, "fail-kitty-before-exec"), []byte("armed"))
			cmd, output := startCrashRPC(t, filepath.Join(bin, "crash-rpc"), hostEnv, crashRequest(t, slicerpc.VerbLaunch, token))
			if err := cmd.Wait(); err != nil {
				t.Fatalf("definite failure helper: %v %s", err, output.Bytes())
			}
			response := rpcResponse(t, output.Bytes())
			record := readOnlyTokenRecord(t, host.state, token)
			if response.Outcome.Status != slicerpc.StatusFailed || record.Status != slicerpc.TokenFailed || markerPresent(record.PreparedSocketPath) {
				t.Fatalf("definite pre-start cleanup not final: response=%+v record=%+v", response, record)
			}
			if replay := rpcResponse(t, runPackagedCrashRPC(t, filepath.Join(bin, "ssh"), redeem, hostEnv, crashRequest(t, slicerpc.VerbTokenReplay, token))); replay.Outcome.Status != slicerpc.StatusFailed {
				t.Fatalf("definite failure replay changed result: %+v", replay)
			}
			assertCrashCardinality(t, "definite_pre_start_failure", root, redeem, zellij, host, hostEnv, niri, ledger, baseline, token, crashCardinalityExpected{status: slicerpc.TokenFailed, stage: "kitty_starting", sessions: 1, kitty: 0, placements: 0, sources: 0, prepared: false})
		})

		// Response loss uses only the shell-inert transport and packaged RPC for
		// both the successful launch whose response is dropped and its replay.
		t.Run("response_loss", func(t *testing.T) {
			token := "matrix-response-loss"
			baseline := captureCrashCardinality(t, root, host.state, niri, ledger)
			mustWrite(t, filepath.Join(faults, "drop-next-launch"), []byte("armed"))
			payload, err := runPackagedCrashRPCRaw(t, filepath.Join(bin, "ssh"), redeem, hostEnv, crashRequest(t, slicerpc.VerbLaunch, token))
			if err == nil {
				t.Fatalf("response loss transport unexpectedly succeeded: %s", payload)
			}
			if record := readOnlyTokenRecord(t, host.state, token); record.Status != slicerpc.TokenLaunched || record.Stage != "committed" {
				t.Fatalf("lost response did not follow durable commit: %+v", record)
			}
			if replay := rpcResponse(t, runPackagedCrashRPC(t, filepath.Join(bin, "ssh"), redeem, hostEnv, crashRequest(t, slicerpc.VerbTokenReplay, token))); replay.Outcome.Status != slicerpc.StatusOK {
				t.Fatalf("response-loss replay=%+v", replay)
			}
			assertCrashCardinality(t, "response_loss", root, redeem, zellij, host, hostEnv, niri, ledger, baseline, token, crashCardinalityExpected{status: slicerpc.TokenLaunched, stage: "committed", sessions: 1, kitty: 1, placements: 1, sources: 1, prepared: true})
			all := readInvocations(t, ledger)
			if countInv(all, "ssh", func(v invocation) bool { return v.Verb == "launch" && v.Token == token && v.Dropped }) != 1 || countInv(all, "ssh", func(v invocation) bool { return v.Verb == "token_replay" && v.Token == token }) != 2 {
				t.Fatalf("response-loss packaged framing totals are not exact: %+v", all)
			}
		})

		// Distinct marker-checked cleanup/final-journal crash. The namespace is gone,
		// the durable ambiguity remains bounded, and replay never guesses or starts.
		t.Run("cleanup_crash", func(t *testing.T) {
			token := "matrix-cleanup-crash"
			baseline := captureCrashCardinality(t, root, host.state, niri, ledger)
			mustWrite(t, filepath.Join(faults, "fail-kitty-before-exec"), []byte("armed"))
			mustWrite(t, filepath.Join(faults, "gate-after-checked-cleanup"), []byte("armed"))
			cmd, _ := startCrashRPC(t, filepath.Join(bin, "crash-rpc"), hostEnv, crashRequest(t, slicerpc.VerbLaunch, token))
			waitInvocations(t, ledger, 1, "cleanup-gated", func(v invocation) bool { return v.Token == token })
			killGatedRPC(t, cmd, root)
			record := readOnlyTokenRecord(t, host.state, token)
			if record.Stage != "kitty_starting" || record.Status != slicerpc.TokenPending || markerPresent(record.PreparedSocketPath) {
				t.Fatalf("cleanup crash not inspectable: %+v", record)
			}
			response := rpcResponse(t, runPackagedCrashRPC(t, filepath.Join(bin, "ssh"), redeem, hostEnv, crashRequest(t, slicerpc.VerbTokenReplay, token)))
			if response.Outcome.Status != slicerpc.StatusPending {
				t.Fatalf("cleanup ambiguity guessed or restarted: %+v", response)
			}
			assertCrashCardinality(t, "cleanup_crash", root, redeem, zellij, host, hostEnv, niri, ledger, baseline, token, crashCardinalityExpected{status: slicerpc.TokenPending, stage: "kitty_starting", sessions: 1, kitty: 0, placements: 0, sources: 0, prepared: false})
		})
	}

	if !niri.sentinelPresent() {
		t.Fatal("sentinel work was lost")
	}
	t.Logf("crash matrix complete stages=%v outcomes=%v runtime=%s retain_opt_in=%t", routedCrashStages, outcomes, time.Since(startedAt).Round(time.Millisecond), retain)
}
