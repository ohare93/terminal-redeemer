package sliceprotocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
)

const fuzzCompleteEnvelope = `{"schema_version":1,"source_host_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","observation":{"quality":"complete","attempted_at":"2026-07-25T12:00:00Z"},"authoritative":{"source_epoch":"11111111-1111-4111-8111-111111111111","revision":1,"observed_at":"2026-07-25T12:00:00Z","workspace_normalization":"unicode-nfkc-fold-v1","live_session_ids":[],"sources":[],"conflicts":[]}}`
const fuzzDegradedEnvelope = `{"schema_version":1,"source_host_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","observation":{"quality":"degraded","attempted_at":"2026-07-25T12:00:01Z","degraded_reasons":[{"code":"niri_replay_timeout"}]}}`

func FuzzInventoryEnvelope(f *testing.F) {
	seeds := [][]byte{
		[]byte(fuzzCompleteEnvelope), []byte(fuzzDegradedEnvelope),
		[]byte(strings.Replace(fuzzCompleteEnvelope, `"revision":1`, `"revision":0`, 1)),
		[]byte(strings.Replace(fuzzCompleteEnvelope, `"revision":1`, `"revision":18446744073709551615`, 1)),
		[]byte(`{"host":"legacy","profile":"default","windows":[]}`),
		[]byte(`{"schema_version":1,"schema_version":1}`),
		[]byte(`{"schema_version":1,"source_host_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","unknown":"\u0000","observation":{}}`),
		append([]byte(`{"unknown":"`), 0xff, '"', '}'),
		[]byte(`{"schema_version":1`), []byte{},
	}
	for _, seed := range seeds {
		f.Add(seed, uint8(0))
	}
	// Modes generate exact protocol size boundaries without checking multi-MiB
	// corpus entries into the repository.
	f.Add([]byte{}, uint8(1))
	f.Add([]byte{}, uint8(2))
	f.Add([]byte("field"), uint8(3))
	f.Add([]byte{}, uint8(4))
	f.Fuzz(func(t *testing.T, input []byte, mode uint8) {
		if len(input) > MaxPayloadBytes+1 {
			return
		}
		payload := append([]byte(nil), input...)
		expectEncodeTooLarge := mode == 4 && len(input) == 0
		if expectEncodeTooLarge {
			payload = fuzzHighCardinalityEnvelope()
		}
		switch mode % 4 {
		case 1:
			payload = append(bytes.Repeat([]byte{' '}, MaxPayloadBytes-2), '{', '}')
		case 2:
			payload = bytes.Repeat([]byte{' '}, MaxPayloadBytes+1)
		case 3:
			payload = []byte(`{"schema_version":1,"schema_version":1}`)
		}
		envelope, err := Decode(bytes.NewReader(payload))
		if mode%4 == 2 && err == nil {
			t.Fatal("accepted over-limit inventory envelope")
		}
		if mode%4 == 3 && err == nil {
			t.Fatal("accepted duplicate inventory key")
		}
		if err != nil {
			return
		}
		var first, second bytes.Buffer
		if err := Encode(&first, envelope); err != nil {
			if expectEncodeTooLarge && errors.Is(err, ErrInvalid) && first.Len() == 0 {
				return
			}
			t.Fatalf("accepted envelope did not re-encode: %v", err)
		}
		if expectEncodeTooLarge {
			t.Fatal("high-cardinality minified envelope encoded above wire cap")
		}
		if first.Len() > MaxPayloadBytes || !utf8.Valid(first.Bytes()) {
			t.Fatalf("encoded inventory exceeded bound or UTF-8 contract: %d", first.Len())
		}
		redecoded, err := Decode(bytes.NewReader(first.Bytes()))
		if err != nil {
			t.Fatalf("encoded inventory did not decode: %v", err)
		}
		if err := Encode(&second, redecoded); err != nil || !bytes.Equal(first.Bytes(), second.Bytes()) {
			t.Fatalf("inventory canonical encoding is not deterministic: %v", err)
		}
	})
}

func fuzzHighCardinalityEnvelope() []byte {
	now := time.Unix(1, 0).UTC()
	authority := Authoritative{SourceEpoch: "11111111-1111-4111-8111-111111111111", Revision: 1, ObservedAt: now, WorkspaceNormalization: WorkspaceNormalization, LiveSessionIDs: []string{}, Sources: []Source{}, Conflicts: make([]Conflict, 100000)}
	for i := range authority.Conflicts {
		authority.Conflicts[i] = Conflict{Code: ConflictSessionMissing}
	}
	payload, _ := json.Marshal(Envelope{SchemaVersion: SchemaVersion, SourceHostID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Observation: Observation{Quality: QualityComplete, AttemptedAt: now}, Authoritative: &authority})
	return payload
}

func FuzzCanonicalHashing(f *testing.F) {
	now := time.Unix(1, 0).UTC()
	sourceA := Source{SourceID: "src_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", RuntimeWindowID: 2, Session: Session{ID: "ses_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Name: "alpha", Status: "active"}, Workspace: Workspace{RuntimeID: 2, Name: "Work", Key: "work"}, Output: &Output{Name: "DP-1", LogicalWidth: 1920, LogicalHeight: 1080, Scale: 1, Transform: "normal"}, Layout: Layout{Mode: "tiled", Position: &Position{Column: 2, Tile: 1}, TileWidth: 900, TileHeight: 700, WindowWidth: 900, WindowHeight: 700}}
	sourceB := sourceA
	sourceB.SourceID, sourceB.Session.ID, sourceB.Session.Name, sourceB.RuntimeWindowID = "src_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", "ses_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", "beta", 1
	sourceB.Workspace = Workspace{RuntimeID: 1, Name: "Dev", Key: "dev"}
	authority := Authoritative{SourceEpoch: "11111111-1111-4111-8111-111111111111", Revision: ^uint64(0), ObservedAt: now, WorkspaceNormalization: WorkspaceNormalization, LiveSessionIDs: []string{sourceA.Session.ID, sourceB.Session.ID}, Sources: []Source{sourceA, sourceB}, Conflicts: []Conflict{{Code: ConflictSessionMissing, SourceID: sourceB.SourceID}, {Code: ConflictKittyProcessUnverified, SourceID: sourceA.SourceID}}}
	permuted, _ := json.Marshal(Envelope{SchemaVersion: SchemaVersion, SourceHostID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Observation: Observation{Quality: QualityComplete, AttemptedAt: now}, Authoritative: &authority})
	f.Add([]byte(fuzzCompleteEnvelope))
	f.Add([]byte(fuzzDegradedEnvelope))
	f.Add(permuted)
	f.Add([]byte(`{"schema_version":1,"schema_version":1}`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		if len(payload) > MaxPayloadBytes {
			return
		}
		envelope, err := Decode(bytes.NewReader(payload))
		if err != nil || envelope.Authoritative == nil {
			return
		}
		first := Canonicalize(*envelope.Authoritative)
		second := Canonicalize(first)
		if !reflect.DeepEqual(first, second) {
			t.Fatal("canonicalization is not idempotent")
		}
		a, errA := SemanticHash(first)
		b, errB := SemanticHash(second)
		if errA != nil || errB != nil || a != b || len(a) != 64 {
			t.Fatalf("semantic hash is not deterministic: %q %q %v %v", a, b, errA, errB)
		}
	})
}

func FuzzWorkspaceNormalization(f *testing.F) {
	for _, seed := range []string{"Dev", "  ＤＥＶ  ", "Straße", "e\u0301", "˚", "Ꮧ", "\x00", "line\nfeed", string([]byte{0xff}), strings.Repeat("x", MaxWorkspaceKeyBytes), strings.Repeat("x", MaxWorkspaceKeyBytes+1)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		if len(name) > MaxWorkspaceKeyBytes*4 {
			return
		}
		key, err := NormalizeWorkspaceName(name)
		if err != nil {
			return
		}
		repeated, repeatErr := NormalizeWorkspaceName(name)
		if repeatErr != nil || repeated != key {
			t.Fatalf("workspace normalization is not deterministic: %q %q %v", key, repeated, repeatErr)
		}
		if !utf8.ValidString(key) || key == "" || len(key) > MaxWorkspaceKeyBytes {
			t.Fatalf("accepted normalization escaped bounds: %q", key)
		}
		for _, r := range key {
			if r == 0 || unicode.IsControl(r) {
				t.Fatalf("accepted workspace control character: %q", key)
			}
		}
		if name == "˚" && key != " \u030a" {
			t.Fatalf("U+02DA published v1 key drifted: %q", key)
		}
		if name == "Ꮧ" && key != "ꮧ" {
			t.Fatalf("Cherokee published v1 key drifted: %q", key)
		}
	})
}

func FuzzDuplicateAndTruncatedJSON(f *testing.F) {
	f.Add([]byte(fuzzCompleteEnvelope), uint8(0))
	f.Add([]byte(`{"value":1}`), uint8(0))
	for mode := uint8(1); mode <= 7; mode++ {
		f.Add([]byte{}, mode)
	}
	f.Fuzz(func(t *testing.T, input []byte, mode uint8) {
		if len(input) > MaxPayloadBytes {
			return
		}
		payload := append([]byte(nil), input...)
		mustReject := mode%8 != 0
		switch mode % 8 {
		case 1:
			payload = []byte(`{"outer":{"value":1,"value":2}}`)
		case 2:
			payload = append([]byte(`{"value":"`), 0xff, '"', '}')
		case 3:
			payload = []byte(`{"number":12`) // number/object truncation
		case 4:
			payload = []byte(`{"array":[1,2`) // array truncation
		case 5:
			payload = []byte(`{"literal":tru`) // literal truncation
		case 6:
			payload = []byte(`{"string":"text`) // string truncation
		case 7:
			payload = []byte(`{"object":{"value":1`) // nested object truncation
		}
		errA := RejectDuplicateKeys(payload)
		errB := RejectDuplicateKeys(append([]byte(nil), payload...))
		if (errA == nil) != (errB == nil) {
			t.Fatal("JSON boundary result is not deterministic")
		}
		if mustReject && errA == nil {
			t.Fatalf("duplicate/UTF-8/truncated JSON class %d accepted: %q", mode%8, payload)
		}
		if !utf8.Valid(payload) && errA == nil {
			t.Fatal("invalid UTF-8 JSON accepted")
		}
	})
}
