package sliceprotocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

const testHostID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
const testSourceID = "src_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
const testSessionID = "ses_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func fixtureAuthority(epoch string, revision uint64, observed time.Time) Authoritative {
	return Authoritative{SourceEpoch: epoch, Revision: revision, ObservedAt: observed, WorkspaceNormalization: WorkspaceNormalization, LiveSessionIDs: []string{testSessionID}, Conflicts: []Conflict{}, Sources: []Source{{
		SourceID: testSourceID, RuntimeWindowID: 42,
		Session:   Session{ID: testSessionID, Name: "project", Status: "active"},
		Workspace: Workspace{RuntimeID: 7, Name: "Dev", Key: "dev"},
		Output:    &Output{Name: "DP-1", LogicalWidth: 1920, LogicalHeight: 1080, Scale: 1, Transform: "Normal"},
		Layout:    Layout{Mode: "tiled", Position: &Position{Column: 1, Tile: 1}, TileWidth: 900, TileHeight: 700, WindowWidth: 900, WindowHeight: 700},
	}}}
}

func TestEncodeDecodeV2AndUnknownAdditiveFields(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	authority := fixtureAuthority("11111111-1111-4111-8111-111111111111", 1, now)
	envelope := Envelope{SchemaVersion: SchemaVersion, SourceHostID: testHostID, Observation: Observation{Quality: QualityComplete, AttemptedAt: now}, Authoritative: &authority}
	var encoded bytes.Buffer
	if err := Encode(&encoded, envelope); err != nil {
		t.Fatal(err)
	}
	payload := strings.Replace(encoded.String(), `"source_host_id": "`+testHostID+`"`, `"source_host_id": "`+testHostID+`", "future_field": {"ok": true}`, 1)
	decoded, err := Decode(strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Authoritative.Revision != 1 || decoded.Authoritative.Sources[0].Session.Name != "project" {
		t.Fatalf("unexpected decode: %+v", decoded)
	}
}

func TestSchema2AllowsOnlyOutputGeometryToBeAbsent(t *testing.T) {
	now := time.Unix(1, 0).UTC()
	authority := fixtureAuthority("11111111-1111-4111-8111-111111111111", 1, now)
	authority.Sources[0].Output = nil
	envelope := Envelope{SchemaVersion: SchemaVersion, SourceHostID: testHostID, Observation: Observation{Quality: QualityComplete, AttemptedAt: now}, Authoritative: &authority}
	var encoded bytes.Buffer
	if err := Encode(&encoded, envelope); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded.String(), `"output"`) {
		t.Fatalf("absent output geometry was serialized: %s", encoded.String())
	}
	decoded, err := Decode(bytes.NewReader(encoded.Bytes()))
	if err != nil || decoded.Authoritative.Sources[0].Output != nil {
		t.Fatalf("headless source did not round trip: %+v %v", decoded, err)
	}
	invalid := authority
	invalid.Sources = append([]Source(nil), authority.Sources...)
	invalid.Sources[0].Layout.WindowWidth = 0
	if err := ValidateAuthoritative(invalid); err == nil {
		t.Fatal("optional output weakened required window layout validation")
	}
}

func TestDecodeRejectsDuplicateKeysLegacyAndUnknownEnums(t *testing.T) {
	cases := []string{
		`{"schema_version":1,"schema_version":1}`,
		`{"host":"legacy","profile":"default","generated_at":"2026-01-01T00:00:00Z","windows":[]}`,
		`{"schema_version":1,"source_host_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","observation":{"quality":"mystery","attempted_at":"2026-01-01T00:00:00Z"}}`,
	}
	for _, payload := range cases {
		if _, err := Decode(strings.NewReader(payload)); err == nil {
			t.Fatalf("accepted hostile payload %s", payload)
		}
	}
}

func TestDecodeRejectsInvalidUTF8IncludingUnknownFields(t *testing.T) {
	payload := append([]byte(`{"schema_version":1,"source_host_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","unknown":"`), 0xff)
	payload = append(payload, []byte(`","observation":{"quality":"degraded","attempted_at":"2026-01-01T00:00:00Z","degraded_reasons":[{"code":"niri_malformed"}]}}`)...)
	if _, err := Decode(bytes.NewReader(payload)); err == nil {
		t.Fatal("accepted invalid UTF-8 in additive field")
	}
	if err := RejectDuplicateKeys(payload); err == nil {
		t.Fatal("raw JSON trust-boundary validation accepted invalid UTF-8")
	}
}

func TestWorkspaceNormalization(t *testing.T) {
	left, err := NormalizeWorkspaceName("  ＤＥＶ  ")
	if err != nil {
		t.Fatal(err)
	}
	right, err := NormalizeWorkspaceName("dev")
	if err != nil {
		t.Fatal(err)
	}
	if left != right || left != "dev" {
		t.Fatalf("normalization mismatch %q %q", left, right)
	}
	if _, err := NormalizeWorkspaceName("\x00"); err == nil {
		t.Fatal("accepted control workspace")
	}
}

func TestWorkspaceNormalizationPublishedV1CompatibilityFixtures(t *testing.T) {
	// These non-idempotent forms pin the published trim -> NFKC -> Fold -> NFKC
	// algorithm. They are deterministic v1 outputs, not permission to migrate an
	// existing key by normalizing that key a second time.
	fixtures := map[string]string{
		"˚": " \u030a", // U+02DA compatibility-normalizes to SPACE + COMBINING RING.
		"Ꮧ": "ꮧ",       // The pinned x/text tables fold Cherokee U+13D7 to U+ABA7.
	}
	for input, want := range fixtures {
		first, err := NormalizeWorkspaceName(input)
		if err != nil || first != want {
			t.Fatalf("published v1 normalization %q = %q, want %q (err=%v)", input, first, want, err)
		}
		second, err := NormalizeWorkspaceName(input)
		if err != nil || second != first {
			t.Fatalf("published v1 normalization is not deterministic for %q: %q %q %v", input, first, second, err)
		}
	}
}

func TestSemanticHashExcludesRevisionAndTimes(t *testing.T) {
	first := fixtureAuthority("11111111-1111-4111-8111-111111111111", 1, time.Unix(1, 0).UTC())
	second := first
	second.Revision = 99
	second.ObservedAt = time.Unix(99, 0).UTC()
	second.SourceEpoch = "22222222-2222-4222-8222-222222222222"
	a, _ := SemanticHash(first)
	b, _ := SemanticHash(second)
	if a != b {
		t.Fatalf("semantic hash includes authority metadata: %s %s", a, b)
	}
	second.LiveSessionIDs = append(second.LiveSessionIDs, "ses_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")
	c, _ := SemanticHash(second)
	if c == a {
		t.Fatal("live-session catalog change did not change hash")
	}
	second.LiveSessionIDs = first.LiveSessionIDs
	second.Sources = append([]Source(nil), second.Sources...)
	second.Sources[0].Layout.WindowWidth++
	c, _ = SemanticHash(second)
	if c == a {
		t.Fatal("semantic change did not change hash")
	}
}

func TestLiveSessionIDValidation(t *testing.T) {
	now := time.Now().UTC()
	valid := fixtureAuthority("11111111-1111-4111-8111-111111111111", 1, now)
	cases := map[string]func(*Authoritative){
		"nil":              func(a *Authoritative) { a.LiveSessionIDs = nil },
		"duplicate":        func(a *Authoritative) { a.LiveSessionIDs = append(a.LiveSessionIDs, testSessionID) },
		"malformed":        func(a *Authoritative) { a.LiveSessionIDs = []string{"ses_bad"} },
		"eligible missing": func(a *Authoritative) { a.LiveSessionIDs = []string{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			a := valid
			a.LiveSessionIDs = append([]string(nil), valid.LiveSessionIDs...)
			mutate(&a)
			if err := ValidateAuthoritative(a); err == nil {
				t.Fatal("accepted invalid live-session authority")
			}
		})
	}
	empty := valid
	empty.Sources = []Source{}
	empty.LiveSessionIDs = []string{}
	if err := ValidateAuthoritative(empty); err != nil {
		t.Fatalf("empty live session list must be valid without eligible sources: %v", err)
	}
}

func TestAcceptorOrderingEpochAndReceiveTimeFreshness(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	epoch1 := "11111111-1111-4111-8111-111111111111"
	authority := fixtureAuthority(epoch1, 1, time.Unix(999999, 0).UTC())
	envelope := Envelope{SchemaVersion: SchemaVersion, SourceHostID: testHostID, Observation: Observation{Quality: QualityComplete, AttemptedAt: now}, Authoritative: &authority}
	first, err := Accept(AcceptanceState{}, envelope, now)
	if err != nil || first.Decision != DecisionAccepted {
		t.Fatalf("first: %+v %v", first, err)
	}
	duplicate, _ := Accept(first.State, envelope, now.Add(time.Second))
	if duplicate.Decision != DecisionDuplicate {
		t.Fatalf("duplicate: %s", duplicate.Decision)
	}
	higherUnchangedAuthority := authority
	higherUnchangedAuthority.Revision = 2
	higherUnchangedAuthority.ObservedAt = authority.ObservedAt.Add(time.Second)
	higherUnchangedEnvelope := envelope
	higherUnchangedEnvelope.Authoritative = &higherUnchangedAuthority
	higherUnchanged, _ := Accept(first.State, higherUnchangedEnvelope, now.Add(2*time.Second))
	if higherUnchanged.Decision != DecisionAccepted || higherUnchanged.State.Revision != 2 || higherUnchanged.State.SemanticHash != first.State.SemanticHash {
		t.Fatalf("higher complete revision with unchanged semantics: %+v", higherUnchanged)
	}
	higherReplay, _ := Accept(higherUnchanged.State, higherUnchangedEnvelope, now.Add(3*time.Second))
	if higherReplay.Decision != DecisionDuplicate || higherReplay.State.Revision != 2 {
		t.Fatalf("same-revision replay: %+v", higherReplay)
	}
	conflictingAuthority := authority
	conflictingAuthority.Sources = append([]Source(nil), authority.Sources...)
	conflictingAuthority.Sources[0].Layout.WindowWidth++
	conflictingEnvelope := envelope
	conflictingEnvelope.Authoritative = &conflictingAuthority
	conflict, _ := Accept(first.State, conflictingEnvelope, now.Add(time.Second))
	if conflict.Decision != DecisionConflict {
		t.Fatalf("same-revision semantic mismatch: %s", conflict.Decision)
	}
	// Struct validation rejects revision zero before ordering; use a higher accepted state instead.
	state := duplicate.State
	state.Revision = 2
	stale, _ := Accept(state, envelope, now.Add(2*time.Second))
	if stale.Decision != DecisionStale {
		t.Fatalf("stale: %s", stale.Decision)
	}
	newAuthority := authority
	newAuthority.SourceEpoch = "22222222-2222-4222-8222-222222222222"
	newEnvelope := envelope
	newEnvelope.Authoritative = &newAuthority
	resync, _ := Accept(duplicate.State, newEnvelope, now.Add(3*time.Second))
	if resync.Decision != DecisionFullResync {
		t.Fatalf("resync: %s", resync.Decision)
	}
	replay, _ := Accept(resync.State, envelope, now.Add(4*time.Second))
	if replay.Decision != DecisionReplay {
		t.Fatalf("replay: %s", replay.Decision)
	}
	if !resync.State.Fresh(now.Add(3*time.Second+500*time.Millisecond), time.Second) {
		t.Fatal("receive-time freshness ignored")
	}
	if resync.State.Fresh(now.Add(5*time.Second), time.Second) {
		t.Fatal("stale authority reported fresh")
	}
}

func TestDegradedPreservesAuthority(t *testing.T) {
	state := AcceptanceState{SourceHostID: testHostID, SourceEpoch: "11111111-1111-4111-8111-111111111111", Revision: 7, SemanticHash: "hash", AuthorityReceivedAt: time.Unix(1, 0)}
	envelope := Envelope{SchemaVersion: SchemaVersion, SourceHostID: testHostID, Observation: Observation{Quality: QualityDegraded, AttemptedAt: time.Unix(2, 0), DegradedReasons: []Reason{{Code: ReasonNiriReplayTimeout}}}}
	result, err := Accept(state, envelope, time.Unix(3, 0))
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != DecisionDegraded || result.State.Revision != 7 || result.State.AuthorityReceivedAt != state.AuthorityReceivedAt {
		t.Fatalf("degraded changed authority: %+v", result)
	}
}

func TestCommittedProtocolFixturesDecode(t *testing.T) {
	for _, path := range []string{"testdata/complete-v2.json", "testdata/degraded-v2.json"} {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Decode(bytes.NewReader(payload)); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
}

func TestCanonicalOrderingUsesExplicitUnnamedLastAndSortedConflicts(t *testing.T) {
	now := time.Unix(1, 0).UTC()
	named := fixtureAuthority("11111111-1111-4111-8111-111111111111", 1, now).Sources[0]
	named.SourceID = "src_ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"
	named.Session.ID = "ses_ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"
	named.Workspace = Workspace{RuntimeID: 9, Name: "\uffff", Key: "\uffff"}
	unnamed := named
	unnamed.SourceID = "src_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	unnamed.Session.ID = "ses_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	unnamed.Workspace = Workspace{RuntimeID: 1}
	authority := Authoritative{SourceEpoch: "11111111-1111-4111-8111-111111111111", Revision: 1, ObservedAt: now, WorkspaceNormalization: WorkspaceNormalization, LiveSessionIDs: []string{named.Session.ID, unnamed.Session.ID}, Sources: []Source{unnamed, named}, Conflicts: []Conflict{{Code: ConflictSessionMissing, SourceID: unnamed.SourceID}, {Code: ConflictKittyProcessUnverified, SourceID: named.SourceID}}}
	canonical := Canonicalize(authority)
	if canonical.LiveSessionIDs[0] != unnamed.Session.ID || canonical.LiveSessionIDs[1] != named.Session.ID {
		t.Fatalf("live session order=%+v", canonical.LiveSessionIDs)
	}
	if canonical.Sources[0].Workspace.Key != "\uffff" || canonical.Sources[1].Workspace.Key != "" {
		t.Fatalf("unnamed workspace did not sort last: %+v", canonical.Sources)
	}
	if canonical.Conflicts[0].Code != ConflictKittyProcessUnverified || canonical.Conflicts[1].Code != ConflictSessionMissing {
		t.Fatalf("conflict order=%+v", canonical.Conflicts)
	}
}

func TestRetiredEpochTombstonesAreExactBoundedAndFailClosedAtCapacity(t *testing.T) {
	now := time.Now().UTC()
	state := AcceptanceState{}
	epochs := make([]string, MaxRetiredEpochTombstones+2)
	for i := 0; i <= MaxRetiredEpochTombstones; i++ {
		epochs[i] = fmt.Sprintf("%08x-0000-4000-8000-%012x", i+1, i+1)
		authority := Authoritative{SourceEpoch: epochs[i], Revision: 1, ObservedAt: now, WorkspaceNormalization: WorkspaceNormalization, LiveSessionIDs: []string{}, Sources: []Source{}, Conflicts: []Conflict{}}
		result, err := Accept(state, Envelope{SchemaVersion: SchemaVersion, SourceHostID: testHostID, Observation: Observation{Quality: QualityComplete, AttemptedAt: now}, Authoritative: &authority}, now)
		if err != nil {
			t.Fatalf("transition %d: %v", i, err)
		}
		state = result.State
	}
	if len(state.RetiredEpochs) != MaxRetiredEpochTombstones {
		t.Fatalf("exact tombstone count=%d", len(state.RetiredEpochs))
	}
	old := Authoritative{SourceEpoch: epochs[0], Revision: 99, ObservedAt: now, WorkspaceNormalization: WorkspaceNormalization, LiveSessionIDs: []string{}, Sources: []Source{}, Conflicts: []Conflict{}}
	result, err := Accept(state, Envelope{SchemaVersion: SchemaVersion, SourceHostID: testHostID, Observation: Observation{Quality: QualityComplete, AttemptedAt: now}, Authoritative: &old}, now)
	if err != nil || result.Decision != DecisionReplay {
		t.Fatalf("old replay decision=%s err=%v", result.Decision, err)
	}
	epochs[len(epochs)-1] = fmt.Sprintf("%08x-0000-4000-8000-%012x", len(epochs)+10, len(epochs)+10)
	novel := Authoritative{SourceEpoch: epochs[len(epochs)-1], Revision: 1, ObservedAt: now, WorkspaceNormalization: WorkspaceNormalization, LiveSessionIDs: []string{}, Sources: []Source{}, Conflicts: []Conflict{}}
	if _, err := Accept(state, Envelope{SchemaVersion: SchemaVersion, SourceHostID: testHostID, Observation: Observation{Quality: QualityComplete, AttemptedAt: now}, Authoritative: &novel}, now); err == nil || !strings.Contains(err.Error(), "maintenance/re-enrollment") {
		t.Fatalf("novel epoch at exact capacity did not fail explicitly: %v", err)
	}
}

func TestEncodeRefusesIndentedExpansionOverWireCapWithoutPartialOutput(t *testing.T) {
	now := time.Unix(1, 0).UTC()
	authority := Authoritative{SourceEpoch: "11111111-1111-4111-8111-111111111111", Revision: 1, ObservedAt: now, WorkspaceNormalization: WorkspaceNormalization, LiveSessionIDs: []string{}, Sources: []Source{}, Conflicts: make([]Conflict, 100000)}
	for i := range authority.Conflicts {
		authority.Conflicts[i] = Conflict{Code: ConflictSessionMissing}
	}
	envelope := Envelope{SchemaVersion: SchemaVersion, SourceHostID: testHostID, Observation: Observation{Quality: QualityComplete, AttemptedAt: now}, Authoritative: &authority}
	minified, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if len(minified) >= MaxPayloadBytes {
		t.Fatalf("regression fixture minified size=%d is not below cap", len(minified))
	}
	var output bytes.Buffer
	if err := Encode(&output, envelope); err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("indented expansion was not rejected: size=%d err=%v", output.Len(), err)
	}
	if output.Len() != 0 {
		t.Fatalf("oversized Encode partially wrote %d caller bytes", output.Len())
	}
}

func TestEncodeOmitsFloatingPositionAndPreservesSpatialFields(t *testing.T) {
	now := time.Unix(1, 0).UTC()
	authority := fixtureAuthority("11111111-1111-4111-8111-111111111111", 1, now)
	authority.Sources[0].Output = &Output{Name: "DP-1", LogicalX: 10, LogicalY: 20, LogicalWidth: 2560, LogicalHeight: 1440, Scale: 1.5, Transform: "Flipped180"}
	authority.Sources[0].Layout = Layout{Mode: "floating", TileWidth: 700, TileHeight: 500, WindowWidth: 680, WindowHeight: 480}
	envelope := Envelope{SchemaVersion: SchemaVersion, SourceHostID: testHostID, Observation: Observation{Quality: QualityComplete, AttemptedAt: now}, Authoritative: &authority}
	var encoded bytes.Buffer
	if err := Encode(&encoded, envelope); err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Authoritative struct {
			Sources []struct {
				Output Output                     `json:"output"`
				Layout map[string]json.RawMessage `json:"layout"`
			} `json:"sources"`
		} `json:"authoritative"`
	}
	if err := json.Unmarshal(encoded.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	out := raw.Authoritative.Sources[0].Output
	if out.LogicalX != 10 || out.LogicalY != 20 || out.LogicalWidth != 2560 || out.LogicalHeight != 1440 || out.Scale != 1.5 || out.Transform != "Flipped180" {
		t.Fatalf("wire output changed: %+v", out)
	}
	layout := raw.Authoritative.Sources[0].Layout
	if _, found := layout["position"]; found {
		t.Fatalf("floating position serialized: %s", encoded.String())
	}
	for _, field := range []string{"tile_width", "tile_height", "window_width", "window_height"} {
		if _, found := layout[field]; !found {
			t.Fatalf("missing floating field %s", field)
		}
	}
}
