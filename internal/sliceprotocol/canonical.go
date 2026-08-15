package sliceprotocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

func Canonicalize(authority Authoritative) Authoritative {
	out := authority
	out.LiveSessionIDs = make([]string, len(authority.LiveSessionIDs))
	copy(out.LiveSessionIDs, authority.LiveSessionIDs)
	out.Sources = make([]Source, len(authority.Sources))
	copy(out.Sources, authority.Sources)
	out.Conflicts = make([]Conflict, len(authority.Conflicts))
	copy(out.Conflicts, authority.Conflicts)
	for i := range out.Sources {
		if out.Sources[i].Output != nil {
			output := *out.Sources[i].Output
			output.Scale = noNegativeZero(output.Scale)
			out.Sources[i].Output = &output
		}
		out.Sources[i].Layout.TileWidth = noNegativeZero(out.Sources[i].Layout.TileWidth)
		out.Sources[i].Layout.TileHeight = noNegativeZero(out.Sources[i].Layout.TileHeight)
	}
	sort.Strings(out.LiveSessionIDs)
	sort.Slice(out.Sources, func(i, j int) bool {
		a, b := out.Sources[i], out.Sources[j]
		aUnnamed, bUnnamed := a.Workspace.Key == "", b.Workspace.Key == ""
		if aUnnamed != bUnnamed {
			return !aUnnamed
		}
		if a.Workspace.Key != b.Workspace.Key {
			return a.Workspace.Key < b.Workspace.Key
		}
		if a.Workspace.RuntimeID != b.Workspace.RuntimeID {
			return a.Workspace.RuntimeID < b.Workspace.RuntimeID
		}
		ap, bp := position(a.Layout), position(b.Layout)
		if ap[0] != bp[0] {
			return ap[0] < bp[0]
		}
		if ap[1] != bp[1] {
			return ap[1] < bp[1]
		}
		return a.SourceID < b.SourceID
	})
	sort.Slice(out.Conflicts, func(i, j int) bool {
		a, b := out.Conflicts[i], out.Conflicts[j]
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.SourceID != b.SourceID {
			return a.SourceID < b.SourceID
		}
		return a.SessionID < b.SessionID
	})
	return out
}

func SortReasons(reasons []Reason) []Reason {
	out := append([]Reason(nil), reasons...)
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	result := out[:0]
	for _, reason := range out {
		if len(result) == 0 || result[len(result)-1].Code != reason.Code {
			result = append(result, reason)
		}
	}
	return result
}

func SemanticHash(authority Authoritative) (string, error) {
	canonical := Canonicalize(authority)
	semantic := struct {
		WorkspaceNormalization string     `json:"workspace_normalization"`
		LiveSessionIDs         []string   `json:"live_session_ids"`
		Sources                []Source   `json:"sources"`
		Conflicts              []Conflict `json:"conflicts"`
	}{canonical.WorkspaceNormalization, canonical.LiveSessionIDs, canonical.Sources, canonical.Conflicts}
	payload, err := json.Marshal(semantic)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func position(layout Layout) [2]int {
	if layout.Position == nil {
		return [2]int{int(^uint(0) >> 1), int(^uint(0) >> 1)}
	}
	return [2]int{layout.Position.Column, layout.Position.Tile}
}

func noNegativeZero(value float64) float64 {
	if value == 0 {
		return 0
	}
	return value
}
