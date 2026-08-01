package slicerpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/niriipc"
	"github.com/jmo/terminal-redeemer/internal/sliceattach"
	"github.com/jmo/terminal-redeemer/internal/sourceinventory"
	"github.com/jmo/terminal-redeemer/internal/zellijlive"
	"golang.org/x/sys/unix"
)

type transactionStub struct {
	session, plan, prepare, kitty, place, cleanup int
	sessionErr, kittyErr, placeErr                error
	sessionStarted, kittyStarted                  bool
}

func (t *transactionStub) EnsureSession(context.Context, TokenRecord) (bool, error) {
	t.session++
	return t.sessionStarted, t.sessionErr
}
func (t *transactionStub) PlanKitty(context.Context, TokenRecord) (sliceattach.ExactSocketIdentity, error) {
	t.plan++
	return sliceattach.ExactSocketIdentity{Path: "/prepared/socket", SocketDevice: 10, SocketInode: 20}, nil
}
func (t *transactionStub) PrepareKitty(_ context.Context, r TokenRecord) (sliceattach.ExactSocketIdentity, error) {
	t.prepare++
	identity := socketIdentityFromRecord(r)
	identity.MarkerDevice = 10
	identity.MarkerInode = 30
	return identity, nil
}
func (t *transactionStub) EnsureKitty(context.Context, TokenRecord) (int, uint64, bool, error) {
	t.kitty++
	return 123, 77, t.kittyStarted, t.kittyErr
}
func (t *transactionStub) Place(context.Context, TokenRecord, uint64) error {
	t.place++
	return t.placeErr
}
func (t *transactionStub) CleanupKitty(context.Context, TokenRecord) error {
	t.cleanup++
	return nil
}

type staticNiri struct {
	state   niriipc.State
	actions []any
}

func (n *staticNiri) Snapshot(context.Context) (niriipc.State, error) { return n.state, nil }
func (n *staticNiri) Action(_ context.Context, a any) error {
	n.actions = append(n.actions, a)
	return nil
}
func namedState() niriipc.State {
	name := "Work"
	output := "DP-1"
	return niriipc.State{Workspaces: []niriipc.Workspace{{ID: 3, Index: 1, Name: &name, Output: &output, IsActive: true, IsFocused: true}, {ID: 4, Index: 2, Output: &output}}}
}
func routedProof(_ context.Context, r TokenRecord) (string, string, error) {
	epoch := "11111111-1111-4111-8111-111111111111"
	id, err := sourceinventory.SourceID(epoch, r.NiriWindowID)
	return id, epoch, err
}
func routedRequest(token string) Request {
	return request(VerbLaunch, LaunchPayload{Token: token, SessionName: StableSessionName(token), WorkspaceName: "Work"})
}
func TestTokenTransactionLockIsCloseOnExec(t *testing.T) {
	store, err := NewTokenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lock, err := store.LockToken("cloexec-regression")
	if err != nil {
		t.Fatal(err)
	}
	defer UnlockToken(lock)
	flags, err := unix.FcntlInt(lock.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("transaction lock can leak into routed child exec: flags=%#x err=%v", flags, err)
	}
}

func TestHostTransactionCommitsExactIdentityAndReplaysWithoutEffects(t *testing.T) {
	store, _ := NewTokenStore(t.TempDir())
	tx := &transactionStub{}
	server := Server{SourceHostID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", SourceEpoch: "11111111-1111-4111-8111-111111111111", SourceFingerprint: strings.Repeat("a", 64), Tokens: store, HostTransaction: tx, ProveCommit: routedProof, Niri: &staticNiri{state: namedState()}, Now: func() time.Time { return time.Unix(10, 0) }}
	first := server.Handle(context.Background(), routedRequest("token-route"))
	if first.Outcome.Status != StatusOK || tx.session != 1 || tx.plan != 1 || tx.prepare != 1 || tx.kitty != 1 || tx.place != 1 || tx.cleanup != 0 {
		t.Fatalf("first=%+v calls=%d/%d/%d/%d/%d/%d", first, tx.session, tx.plan, tx.prepare, tx.kitty, tx.place, tx.cleanup)
	}
	record, err := store.Read("token-route")
	if err != nil || record.Status != TokenLaunched || record.Stage != "committed" || record.SourceID == "" || record.SourceEpoch == "" || record.NiriWindowID != 77 || record.KittyPID != 123 {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	again := server.Handle(context.Background(), routedRequest("token-route"))
	if again.Outcome.Status != StatusOK || tx.session != 1 || tx.plan != 1 || tx.prepare != 1 || tx.kitty != 1 || tx.place != 1 || tx.cleanup != 0 {
		t.Fatalf("replay=%+v calls=%d/%d/%d/%d/%d/%d", again, tx.session, tx.plan, tx.prepare, tx.kitty, tx.place, tx.cleanup)
	}
}
func TestHostTransactionCleansPreparedNamespaceOnlyOnDefinitePreStartFailure(t *testing.T) {
	store, _ := NewTokenStore(t.TempDir())
	tx := &transactionStub{kittyErr: errors.New("Kitty exec failed")}
	server := Server{SourceHostID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", SourceEpoch: "11111111-1111-4111-8111-111111111111", SourceFingerprint: strings.Repeat("a", 64), Tokens: store, HostTransaction: tx, ProveCommit: routedProof, Niri: &staticNiri{state: namedState()}}
	response := server.Handle(context.Background(), routedRequest("prestart-failure"))
	if response.Outcome.Status != StatusFailed || tx.cleanup != 1 || tx.place != 0 {
		t.Fatalf("response=%+v calls=%+v", response, tx)
	}
	replay := server.Handle(context.Background(), routedRequest("prestart-failure"))
	if replay.Outcome.Status != StatusFailed || tx.cleanup != 1 || tx.kitty != 1 {
		t.Fatalf("replay=%+v calls=%+v", replay, tx)
	}
}

func TestHostTransactionPendingResumesSameSessionAfterAmbiguity(t *testing.T) {
	store, _ := NewTokenStore(t.TempDir())
	tx := &transactionStub{sessionStarted: true, sessionErr: errors.New("lost after create")}
	server := Server{SourceHostID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", SourceEpoch: "11111111-1111-4111-8111-111111111111", SourceFingerprint: strings.Repeat("a", 64), Tokens: store, HostTransaction: tx, ProveCommit: routedProof, Niri: &staticNiri{state: namedState()}}
	first := server.Handle(context.Background(), routedRequest("token-ambiguous"))
	if first.Outcome.Status != StatusPending {
		t.Fatalf("first=%+v", first)
	}
	record, _ := store.Read("token-ambiguous")
	if record.Stage != "session_starting" || record.SessionName != StableSessionName("token-ambiguous") {
		t.Fatalf("record=%+v", record)
	}
	tx.sessionErr = nil
	tx.sessionStarted = false
	replay := server.Handle(context.Background(), request(VerbTokenReplay, TokenPayload{Token: "token-ambiguous", SessionName: StableSessionName("token-ambiguous"), WorkspaceName: "Work"}))
	if replay.Outcome.Status != StatusOK || tx.session != 2 || tx.kitty != 1 {
		t.Fatalf("replay=%+v calls=%d/%d", replay, tx.session, tx.kitty)
	}
}
func TestHostTransactionRejectsMetadataConflictAndDefiniteFailure(t *testing.T) {
	store, _ := NewTokenStore(t.TempDir())
	tx := &transactionStub{sessionErr: errors.New("missing executable")}
	server := Server{SourceHostID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", SourceEpoch: "11111111-1111-4111-8111-111111111111", SourceFingerprint: strings.Repeat("a", 64), Tokens: store, HostTransaction: tx, Niri: &staticNiri{state: namedState()}}
	first := server.Handle(context.Background(), routedRequest("token-failed"))
	if first.Outcome.Status != StatusFailed {
		t.Fatalf("first=%+v", first)
	}
	other := routedRequest("token-failed")
	other.Payload, _ = json.Marshal(LaunchPayload{Token: "token-failed", SessionName: StableSessionName("token-failed"), WorkspaceName: "Else"})
	response := server.Handle(context.Background(), other)
	if response.Outcome.Code != "launch_identity_conflict" {
		t.Fatalf("response=%+v", response)
	}
}
func TestEpochRotationBlocksEveryResumedEffectfulStageBeforeAdvancement(t *testing.T) {
	for _, stage := range []string{"pending", "session_starting", "session_created", "kitty_started"} {
		t.Run(stage, func(t *testing.T) {
			store, _ := NewTokenStore(t.TempDir())
			oldEpoch := "11111111-1111-4111-8111-111111111111"
			oldFingerprint := strings.Repeat("a", 64)
			token := "rotated-" + stage
			record, _, err := store.CreatePendingRouted("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", oldEpoch, oldFingerprint, token, StableSessionName(token), "Work", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			record.Stage = stage
			if stage == "kitty_started" {
				record.PreparedSocketPath = "/prepared/socket"
				record.PreparedSocketDevice = 10
				record.PreparedSocketInode = 20
				record.PreparedMarkerDevice = 10
				record.PreparedMarkerInode = 30
				record.KittyPID = 12
				record.NiriWindowID = 44
			}
			if err = store.Update(record); err != nil {
				t.Fatal(err)
			}
			tx := &transactionStub{}
			niri := &staticNiri{state: namedState()}
			server := Server{Tokens: store, SourceEpoch: "22222222-2222-4222-8222-222222222222", SourceFingerprint: strings.Repeat("b", 64), HostTransaction: tx, Niri: niri, ProveCommit: routedProof}
			response := server.Handle(context.Background(), request(VerbTokenReplay, TokenPayload{Token: token, SessionName: record.SessionName, WorkspaceName: record.WorkspaceName}))
			if response.Outcome.Status != StatusPending || tx.session != 0 || tx.kitty != 0 || tx.place != 0 || len(niri.actions) != 0 {
				t.Fatalf("response=%+v effects=%d/%d/%d actions=%d", response, tx.session, tx.kitty, tx.place, len(niri.actions))
			}
			saved, err := store.Read(token)
			if err != nil || saved.Stage != stage || saved.TransactionEpoch != oldEpoch || saved.TransactionFingerprint != oldFingerprint {
				t.Fatalf("saved=%+v err=%v", saved, err)
			}
		})
	}
}

type crashReplayTransaction struct {
	plan, prepare, starts, adopts, place, cleanup int
	identity                                      sliceattach.ExactSocketIdentity
}

func (t *crashReplayTransaction) EnsureSession(context.Context, TokenRecord) (bool, error) {
	return false, nil
}
func (t *crashReplayTransaction) PlanKitty(context.Context, TokenRecord) (sliceattach.ExactSocketIdentity, error) {
	t.plan++
	return sliceattach.ExactSocketIdentity{Path: "/prepared/crash-replay", SocketDevice: 71, SocketInode: 81}, nil
}
func (t *crashReplayTransaction) PrepareKitty(_ context.Context, r TokenRecord) (sliceattach.ExactSocketIdentity, error) {
	t.prepare++
	t.identity = socketIdentityFromRecord(r)
	t.identity.MarkerDevice, t.identity.MarkerInode = 71, 91
	return t.identity, nil
}
func (t *crashReplayTransaction) EnsureKitty(_ context.Context, r TokenRecord) (int, uint64, bool, error) {
	if got := socketIdentityFromRecord(r); got != t.identity {
		return 0, 0, true, errors.New("replay switched prepared namespace identity")
	}
	if r.Stage == "kitty_prepared" {
		t.starts++
		return 123, 0, true, errors.New("crash after Kitty start")
	}
	t.adopts++
	return 123, 77, false, nil
}
func (t *crashReplayTransaction) Place(context.Context, TokenRecord, uint64) error {
	t.place++
	return nil
}
func (t *crashReplayTransaction) CleanupKitty(_ context.Context, r TokenRecord) error {
	if got := socketIdentityFromRecord(r); got != t.identity {
		return errors.New("cleanup identity changed")
	}
	t.cleanup++
	return nil
}

func TestPreparedNamespaceIdentitySurvivesCrashReplayUntilCommitProof(t *testing.T) {
	store, err := NewTokenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tx := &crashReplayTransaction{}
	proof := false
	server := Server{
		SourceHostID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", SourceEpoch: "11111111-1111-4111-8111-111111111111",
		SourceFingerprint: strings.Repeat("a", 64), Tokens: store, HostTransaction: tx,
		Niri: &staticNiri{state: namedState()}, ProveCommit: func(ctx context.Context, r TokenRecord) (string, string, error) {
			if !proof {
				return "", "", errors.New("child source not attached yet")
			}
			return routedProof(ctx, r)
		},
	}
	first := server.Handle(context.Background(), routedRequest("crash-replay"))
	if first.Outcome.Status != StatusPending || tx.starts != 1 || tx.adopts != 0 || tx.cleanup != 0 {
		t.Fatalf("first=%+v effects=%+v", first, tx)
	}
	saved, err := store.Read("crash-replay")
	if err != nil || saved.Stage != "kitty_starting" || socketIdentityFromRecord(saved) != tx.identity {
		t.Fatalf("saved=%+v identity=%+v err=%v", saved, tx.identity, err)
	}
	replay := request(VerbTokenReplay, TokenPayload{Token: saved.Token, SessionName: saved.SessionName, WorkspaceName: saved.WorkspaceName})
	second := server.Handle(context.Background(), replay)
	if second.Outcome.Status != StatusPending || tx.starts != 1 || tx.adopts != 1 || tx.plan != 1 || tx.prepare != 1 || tx.cleanup != 0 {
		t.Fatalf("second=%+v effects=%+v", second, tx)
	}
	saved, err = store.Read(saved.Token)
	if err != nil || saved.Stage != "placed" || socketIdentityFromRecord(saved) != tx.identity {
		t.Fatalf("placed=%+v identity=%+v err=%v", saved, tx.identity, err)
	}
	proof = true
	third := server.Handle(context.Background(), replay)
	if third.Outcome.Status != StatusOK || tx.starts != 1 || tx.adopts != 1 || tx.cleanup != 0 {
		t.Fatalf("third=%+v effects=%+v", third, tx)
	}
}

func TestPlacedReplayRequiresFreshCommitProof(t *testing.T) {
	store, _ := NewTokenStore(t.TempDir())
	now := time.Now()
	fingerprint := strings.Repeat("a", 64)
	record, _, err := store.CreatePendingRouted("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "11111111-1111-4111-8111-111111111111", fingerprint, "proof-token", StableSessionName("proof-token"), "Work", now)
	if err != nil {
		t.Fatal(err)
	}
	record.Stage = "placed"
	record.PreparedSocketPath = "/prepared/socket"
	record.PreparedSocketDevice = 10
	record.PreparedSocketInode = 20
	record.PreparedMarkerDevice = 10
	record.PreparedMarkerInode = 30
	record.KittyPID = 12
	record.NiriWindowID = 44
	if err = store.Update(record); err != nil {
		t.Fatal(err)
	}
	proofOK := false
	server := Server{Tokens: store, SourceEpoch: "11111111-1111-4111-8111-111111111111", SourceFingerprint: fingerprint, HostTransaction: &transactionStub{}, ProveCommit: func(_ context.Context, r TokenRecord) (string, string, error) {
		if !proofOK {
			return "", "", errors.New("epoch rotated")
		}
		return routedProof(context.Background(), r)
	}}
	request := request(VerbTokenReplay, TokenPayload{Token: record.Token, SessionName: record.SessionName, WorkspaceName: record.WorkspaceName})
	first := server.Handle(context.Background(), request)
	if first.Outcome.Status != StatusPending {
		t.Fatalf("first=%+v", first)
	}
	saved, _ := store.Read(record.Token)
	if saved.Stage != "placed" || saved.Status != TokenPending {
		t.Fatalf("saved=%+v", saved)
	}
	proofOK = true
	second := server.Handle(context.Background(), request)
	if second.Outcome.Status != StatusOK {
		t.Fatalf("second=%+v", second)
	}
}

func TestProofCommittedRequiresMatchingFinalProofBeforeCommit(t *testing.T) {
	store, _ := NewTokenStore(t.TempDir())
	fingerprint := strings.Repeat("a", 64)
	record, _, err := store.CreatePendingRouted("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "11111111-1111-4111-8111-111111111111", fingerprint, "final-proof-token", StableSessionName("final-proof-token"), "Work", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	record.Stage = "proof_committed"
	record.PreparedSocketPath = "/prepared/socket"
	record.PreparedSocketDevice, record.PreparedSocketInode = 10, 20
	record.PreparedMarkerDevice, record.PreparedMarkerInode = 10, 30
	record.KittyPID, record.NiriWindowID = 12, 44
	record.SourceID, record.SourceEpoch, err = routedProof(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(record); err != nil {
		t.Fatal(err)
	}
	proofCalls := 0
	server := Server{Tokens: store, SourceEpoch: record.TransactionEpoch, SourceFingerprint: fingerprint, HostTransaction: &transactionStub{}, ProveCommit: func(ctx context.Context, current TokenRecord) (string, string, error) {
		proofCalls++
		if proofCalls == 1 {
			return "wrong-source", current.SourceEpoch, nil
		}
		return routedProof(ctx, current)
	}}
	replay := request(VerbTokenReplay, TokenPayload{Token: record.Token, SessionName: record.SessionName, WorkspaceName: record.WorkspaceName})
	if first := server.Handle(context.Background(), replay); first.Outcome.Status != StatusPending {
		t.Fatalf("mismatched final proof committed: %+v", first)
	}
	if saved, _ := store.Read(record.Token); saved.Stage != "proof_committed" || saved.Status != TokenPending {
		t.Fatalf("mismatched final proof changed journal: %+v", saved)
	}
	if second := server.Handle(context.Background(), replay); second.Outcome.Status != StatusOK || proofCalls != 2 {
		t.Fatalf("matching final proof did not commit: response=%+v calls=%d", second, proofCalls)
	}
}

func TestFreshExactSessionCollisionAndLongSocketPathFailBeforeCreate(t *testing.T) {
	root := t.TempDir()
	record := TokenRecord{StorageVersion: 1, Token: "collision", HostTerminalID: "term_c", Status: TokenPending, SessionName: StableSessionName("collision"), WorkspaceName: "Work", Stage: "pending", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	calls := [][]string{}
	tx := DirectHostTransaction{SelfCommand: "/redeem", ZellijCommand: "/zellij", KittyCommand: "/kitty", Environment: map[string]string{"NIRI_SOCKET": "/run/niri", "WAYLAND_DISPLAY": "w", "XDG_RUNTIME_DIR": "/run/u"}, CreationCacheRoot: filepath.Join(root, "create"), ShimCache: filepath.Join(root, "shim"), Run: func(_ context.Context, _ string, args, _ []string) ([]byte, bool, error) {
		calls = append(calls, append([]string(nil), args...))
		if args[0] == "--version" {
			return []byte("zellij 0.44.3"), true, nil
		}
		return []byte(record.SessionName + "\n"), true, nil
	}}
	started, err := tx.EnsureSession(context.Background(), record)
	if err == nil || started || len(calls) != 2 {
		t.Fatalf("collision started=%v calls=%v err=%v", started, calls, err)
	}
	for _, explicit := range []bool{true, false} {
		calls = nil
		if explicit {
			tx.ZellijSocketDir = "/" + strings.Repeat("x", 200)
		} else {
			tx.ZellijSocketDir = ""
			tx.Environment["XDG_RUNTIME_DIR"] = "/" + strings.Repeat("y", 200)
		}
		if started, err = tx.EnsureSession(context.Background(), record); err == nil || started || len(calls) != 0 {
			t.Fatalf("explicit=%v long path started=%v calls=%v err=%v", explicit, started, calls, err)
		}
	}
}

func TestHostAttachTokenFitsZellijContractSocketPath(t *testing.T) {
	session := StableSessionName("token")
	token := hostAttachToken(session)
	path := filepath.Join("/run/user/1000/zellij", ".trh", "att-"+token, zellijlive.SocketContractDir, session)
	if len(token) != 15 || len(path) > zellijlive.MaxSocketPathBytes {
		t.Fatalf("token=%q path bytes=%d", token, len(path))
	}
}

func TestDirectHostTransactionNeverRepeatsUncertainCreateOrKittyStart(t *testing.T) {
	root := t.TempDir()
	calls := 0
	tx := DirectHostTransaction{SelfCommand: "/redeem", ZellijCommand: "/zellij", KittyCommand: "/kitty", Environment: map[string]string{"NIRI_SOCKET": "/run/niri", "WAYLAND_DISPLAY": "w", "XDG_RUNTIME_DIR": "/run/u"}, CreationCacheRoot: filepath.Join(root, "create"), ShimCache: filepath.Join(root, "shim"), Niri: &staticNiri{state: namedState()}, Run: func(_ context.Context, _ string, args, _ []string) ([]byte, bool, error) {
		calls++
		if args[0] == "--version" {
			return []byte("zellij 0.44.3"), true, nil
		}
		return []byte("No active zellij sessions found"), true, errors.New("none")
	}, StartKitty: func(context.Context, string, []string, []string) (int, bool, error) {
		t.Fatal("uncertain Kitty was repeated")
		return 0, false, nil
	}}
	record := TokenRecord{StorageVersion: 1, Token: "token", HostTerminalID: "term_x", Status: TokenPending, SessionName: StableSessionName("token"), WorkspaceName: "Work", Stage: "session_starting", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	started, err := tx.EnsureSession(context.Background(), record)
	if err == nil || !started || calls != 2 {
		t.Fatalf("started=%v calls=%d err=%v", started, calls, err)
	}
	record.Stage = "kitty_starting"
	_, _, started, err = tx.EnsureKitty(context.Background(), record)
	if err == nil || !started {
		t.Fatalf("kitty started=%v err=%v", started, err)
	}
}

func TestDirectHostTransactionSourceProofBeforeDelayedHelperConnectRetainsNamespaceUntilExit(t *testing.T) {
	state := namedState()
	niri := &staticNiri{state: state}
	var commands [][]string
	var envs [][]string
	created := false
	postCreateLists := 0
	connectNow := make(chan struct{})
	connected := make(chan error, 1)
	exitClient := make(chan struct{})
	helperDone := make(chan sliceattach.Outcome, 1)
	helperArgs := make(chan []string, 1)
	root, err := os.MkdirTemp("/tmp", "r")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	socketBase := filepath.Join(root, "z")
	versionDir := filepath.Join(socketBase, zellijlive.SocketContractDir)
	if err := os.MkdirAll(versionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	session := StableSessionName("token")
	record := TokenRecord{StorageVersion: 1, Token: "token", HostTerminalID: "term_exact", Status: TokenPending, SessionName: session, WorkspaceName: "Work", Stage: "session_created", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	listener, err := net.Listen("unix", filepath.Join(versionDir, session))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var tx DirectHostTransaction
	tx = DirectHostTransaction{SelfCommand: "/store/redeem", ZellijCommand: "/store/zellij", KittyCommand: "/store/kitty", Environment: map[string]string{"NIRI_SOCKET": "/run/niri.sock", "WAYLAND_DISPLAY": "wayland-1", "XDG_RUNTIME_DIR": "/run/user/1"}, ZellijSocketDir: socketBase, Niri: niri, CreationCacheRoot: filepath.Join(root, "create"), ShimCache: filepath.Join(root, "shim"), Run: func(_ context.Context, command string, args, env []string) ([]byte, bool, error) {
		commands = append(commands, append([]string{command}, args...))
		envs = append(envs, append([]string(nil), env...))
		if reflect.DeepEqual(args, []string{"--version"}) {
			return []byte("zellij 0.44.3\n"), true, nil
		}
		if reflect.DeepEqual(args, []string{"list-sessions", "--short"}) {
			if created {
				postCreateLists++
				if postCreateLists == 1 {
					// Reproduce the real fresh-Nix window: create-background has
					// returned and the socket exists, but catalog publication lags.
					return nil, true, nil
				}
				return []byte(StableSessionName("token") + "\n"), true, nil
			}
			return []byte("No active zellij sessions found"), true, errors.New("none")
		}
		if len(args) > 0 && args[0] == "attach" {
			created = true
		}
		return nil, true, nil
	}, StartKitty: func(_ context.Context, command string, args, env []string) (int, bool, error) {
		commands = append(commands, append([]string{command}, args...))
		envs = append(envs, append([]string(nil), env...))
		for _, value := range env {
			if strings.HasPrefix(value, "ZELLIJ_SOCKET_DIR=") || strings.HasPrefix(value, "XDG_CACHE_HOME=") {
				t.Fatalf("server leaked client isolation into Kitty env=%q", env)
			}
		}
		if !reflect.DeepEqual(args, hostKittyArgs(tx, record)) {
			t.Fatalf("Kitty did not start exact packaged helper argv=%q", args)
		}
		helperIdentity := socketIdentityFromRecord(record)
		helperSession := record.SessionName
		helperCommand := tx.ZellijCommand
		helperCache := filepath.Join(tx.ShimCache, "attach-"+helperSession)
		go func() {
			out := (sliceattach.PreparedWrapper{
				Command: helperCommand, Session: helperSession, Identity: helperIdentity,
				ShimCache: helperCache, Version: func(context.Context, string) error { return nil },
				Run: func(_ context.Context, command string, args, env []string, _ io.Reader, _, _ io.Writer) error {
					helperArgs <- append([]string{command}, args...)
					<-connectNow // source-shaped process proof exists before this first lookup
					conn, dialErr := net.DialTimeout("unix", filepath.Join(helperIdentity.Path, zellijlive.SocketContractDir, helperSession), 100*time.Millisecond)
					if dialErr == nil {
						_ = conn.Close()
					}
					connected <- dialErr
					<-exitClient
					return nil
				},
			}).Attach(context.Background())
			helperDone <- out
		}()
		pid := 42
		id := uint64(8)
		niri.state.Windows = []niriipc.Window{{ID: id, PID: pid, AppID: hostAppID("term_exact")}}
		return pid, true, nil
	}, PollInterval: time.Millisecond}
	tx.ReadProcess = func(int) (string, []string, error) {
		return tx.KittyCommand, append([]string{tx.KittyCommand}, hostKittyArgs(tx, record)...), nil
	}
	started, err := tx.EnsureSession(context.Background(), record)
	if err != nil || !started {
		t.Fatalf("session started=%v err=%v", started, err)
	}
	planned, err := tx.PlanKitty(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	if postCreateLists < 2 {
		t.Fatalf("session plan did not wait through transient catalog omission: calls=%d", postCreateLists)
	}
	record.PreparedSocketPath, record.PreparedSocketDevice, record.PreparedSocketInode = planned.Path, planned.SocketDevice, planned.SocketInode
	prepared, err := tx.PrepareKitty(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	record.PreparedMarkerDevice, record.PreparedMarkerInode = prepared.MarkerDevice, prepared.MarkerInode
	record.Stage = "kitty_prepared"
	pid, wid, started, err := tx.EnsureKitty(context.Background(), record)
	if err != nil || !started || pid != 42 || wid != 8 {
		t.Fatalf("kitty=%d/%d/%v err=%v", pid, wid, started, err)
	}
	// Kitty and helper argv are already source-shaped, but the helper's client is
	// deliberately delayed before its first socket lookup. Proof must not clean.
	record.KittyPID, record.NiriWindowID = pid, wid
	workspaceID := uint64(3)
	niri.state.Windows[0].WorkspaceID = &workspaceID
	if err := tx.ProveWindow(context.Background(), record, "Work"); err != nil {
		t.Fatalf("source-shaped process/window proof failed: %v", err)
	}
	tx.ReadProcess = func(int) (string, []string, error) {
		return tx.KittyCommand, []string{tx.KittyCommand, "--config", "NONE", tx.ZellijCommand, "attach", record.SessionName}, nil
	}
	if err := tx.ProveWindow(context.Background(), record, "Work"); err == nil {
		t.Fatal("direct Zellij argv was accepted instead of exact packaged helper proof")
	}
	if _, err := os.Lstat(record.PreparedSocketPath); err != nil {
		t.Fatalf("source-shaped proof removed namespace before lookup: %v", err)
	}
	close(connectNow)
	if err := <-connected; err != nil {
		t.Fatalf("delayed helper client could not use retained namespace: %v", err)
	}
	if _, err := os.Lstat(record.PreparedSocketPath); err != nil {
		t.Fatalf("helper removed namespace while client remained attached: %v", err)
	}
	close(exitClient)
	if out := <-helperDone; out.Status != sliceattach.StatusDetached {
		t.Fatalf("helper outcome=%+v", out)
	}
	if _, err := os.Lstat(record.PreparedSocketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("helper did not clean after client exit: %v", err)
	}
	ordinary, err := net.DialTimeout("unix", filepath.Join(versionDir, session), 100*time.Millisecond)
	if err != nil {
		t.Fatalf("prepared namespace cleanup destroyed host session: %v", err)
	}
	_ = ordinary.Close()
	wantCreate := []string{"/store/zellij", "attach", "--create-background", record.SessionName}
	if !reflect.DeepEqual(commands[2], wantCreate) {
		t.Fatalf("create argv=%q", commands[2])
	}
	kitty := commands[len(commands)-1]
	wantKitty := append([]string{"/store/kitty"}, hostKittyArgs(tx, record)...)
	if !reflect.DeepEqual(kitty, wantKitty) {
		t.Fatalf("kitty argv=%q want=%q", kitty, wantKitty)
	}
	wantHelper := []string{"/store/zellij", "attach", record.SessionName, "options", "--on-force-close", "detach"}
	if got := <-helperArgs; !reflect.DeepEqual(got, wantHelper) {
		t.Fatalf("helper child argv=%q want=%q", got, wantHelper)
	}
	for _, env := range envs {
		for _, value := range env {
			if strings.HasPrefix(value, "ZELLIJ=") || strings.HasPrefix(value, "HOME=") {
				t.Fatalf("unsafe env=%q", env)
			}
		}
	}
}

func TestDirectHostTransactionExactSocketDisappearanceRetainsIsolatedNamespaceWithoutPrefixFallback(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "r")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	socketBase := filepath.Join(root, "z")
	versionDir := filepath.Join(socketBase, zellijlive.SocketContractDir)
	if err := os.MkdirAll(versionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	record := TokenRecord{StorageVersion: 1, Token: "race-token", HostTerminalID: "term_race", Status: TokenPending, SessionName: StableSessionName("race-token"), WorkspaceName: "Work", Stage: "session_created", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	exact, err := net.Listen("unix", filepath.Join(versionDir, record.SessionName))
	if err != nil {
		t.Fatal(err)
	}
	siblingName := record.SessionName + "-sibling"
	sibling, err := net.Listen("unix", filepath.Join(versionDir, siblingName))
	if err != nil {
		t.Fatal(err)
	}
	defer sibling.Close()
	startedKitty := 0
	const kittyPID = 77
	niri := &staticNiri{state: namedState()}
	var tx DirectHostTransaction
	tx = DirectHostTransaction{
		SelfCommand: "/redeem", ZellijCommand: "/zellij", KittyCommand: "/kitty", ZellijSocketDir: socketBase,
		Environment: map[string]string{"NIRI_SOCKET": "/run/niri", "WAYLAND_DISPLAY": "w", "XDG_RUNTIME_DIR": root},
		Niri:        niri, CreationCacheRoot: filepath.Join(root, "create"), ShimCache: filepath.Join(root, "shim"),
		Run: func(_ context.Context, _ string, args, _ []string) ([]byte, bool, error) {
			if reflect.DeepEqual(args, []string{"list-sessions", "--short"}) {
				return []byte(record.SessionName + "\n" + siblingName + "\n"), true, nil
			}
			return nil, true, nil
		},
		StartKitty: func(_ context.Context, _ string, args []string, env []string) (int, bool, error) {
			startedKitty++
			for _, value := range env {
				if strings.HasPrefix(value, "ZELLIJ_SOCKET_DIR=") || strings.HasPrefix(value, "XDG_CACHE_HOME=") {
					t.Fatalf("server leaked helper-owned isolation env=%q", env)
				}
			}
			if !reflect.DeepEqual(args, hostKittyArgs(tx, record)) {
				t.Fatalf("unexpected helper argv=%q", args)
			}
			if err := exact.Close(); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(filepath.Join(record.PreparedSocketPath, zellijlive.SocketContractDir))
			if err != nil || len(entries) != 1 || entries[0].Name() != record.SessionName {
				t.Fatalf("isolated entries=%v err=%v", entries, err)
			}
			if _, err := os.Stat(filepath.Join(record.PreparedSocketPath, zellijlive.SocketContractDir, siblingName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("prefix sibling leaked into exact namespace: %v", err)
			}
			conn, err := net.DialTimeout("unix", filepath.Join(record.PreparedSocketPath, zellijlive.SocketContractDir, record.SessionName), 20*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				t.Fatal("dead exact socket remained attachable")
			}
			niri.state.Windows = []niriipc.Window{{ID: 88, PID: kittyPID, AppID: hostAppID(record.HostTerminalID)}}
			return kittyPID, true, nil
		},
		PollInterval: time.Millisecond,
	}
	tx.ReadProcess = func(int) (string, []string, error) {
		return tx.KittyCommand, append([]string{tx.KittyCommand}, hostKittyArgs(tx, record)...), nil
	}
	planned, err := tx.PlanKitty(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	record.PreparedSocketPath, record.PreparedSocketDevice, record.PreparedSocketInode = planned.Path, planned.SocketDevice, planned.SocketInode
	prepared, err := tx.PrepareKitty(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	record.PreparedMarkerDevice, record.PreparedMarkerInode = prepared.MarkerDevice, prepared.MarkerInode
	record.Stage = "kitty_prepared"
	pid, wid, started, err := tx.EnsureKitty(context.Background(), record)
	if err != nil || !started || pid != kittyPID || wid != 88 || startedKitty != 1 || len(niri.state.Windows) != 1 {
		t.Fatalf("pid=%d wid=%d started=%v calls=%d err=%v", pid, wid, started, startedKitty, err)
	}
	if _, err := os.Lstat(record.PreparedSocketPath); err != nil {
		t.Fatalf("unproven exact namespace was removed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(record.PreparedSocketPath, zellijlive.SocketContractDir, siblingName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prefix sibling appeared in retained namespace: %v", err)
	}
}
