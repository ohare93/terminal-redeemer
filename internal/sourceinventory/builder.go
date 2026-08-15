package sourceinventory

import (
	"context"
	"fmt"
	"strings"

	"github.com/jmo/terminal-redeemer/internal/niriipc"
	"github.com/jmo/terminal-redeemer/internal/sliceprotocol"
	"github.com/jmo/terminal-redeemer/internal/zellijlive"
)

type Builder struct{ Processes zellijlive.ProcessObserver }

type candidate struct {
	source  sliceprotocol.Source
	window  niriipc.Window
	session zellijlive.Session
}

func (builder Builder) Build(ctx context.Context, epoch string, state niriipc.State, catalog zellijlive.Catalog) ([]sliceprotocol.Source, []sliceprotocol.Conflict, error) {
	if builder.Processes == nil {
		builder.Processes = zellijlive.ProcObserver{}
	}
	workspaces := make(map[uint64]niriipc.Workspace, len(state.Workspaces))
	keys := map[uint64]string{}
	spellings := map[string]map[string]struct{}{}
	for _, workspace := range state.Workspaces {
		workspaces[workspace.ID] = workspace
		if workspace.Name != nil {
			key, err := sliceprotocol.NormalizeWorkspaceName(*workspace.Name)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid workspace metadata")
			}
			keys[workspace.ID] = key
			if spellings[key] == nil {
				spellings[key] = map[string]struct{}{}
			}
			spellings[key][*workspace.Name] = struct{}{}
		}
	}
	colliding := map[string]struct{}{}
	for key, values := range spellings {
		if len(values) > 1 {
			colliding[key] = struct{}{}
		}
	}

	candidates := make([]candidate, 0)
	conflicts := make([]sliceprotocol.Conflict, 0)
	for _, window := range state.Windows {
		sourceID, err := SourceID(epoch, window.ID)
		if err != nil {
			return nil, nil, err
		}
		hintKitty := strings.EqualFold(strings.TrimSpace(window.AppID), "kitty") || strings.Contains(strings.ToLower(window.AppID), "kitty")
		evidence, observeErr := builder.Processes.Observe(ctx, window.PID)
		if observeErr != nil {
			return nil, nil, fmt.Errorf("%s: %w", sliceprotocol.ReasonProcessObservationIncomplete, observeErr)
		}
		if !evidence.KittyVerified {
			if hintKitty {
				conflicts = append(conflicts, sliceprotocol.Conflict{Code: sliceprotocol.ConflictKittyProcessUnverified, SourceID: sourceID})
			}
			continue
		}
		if len(evidence.Candidates) == 0 {
			conflicts = append(conflicts, sliceprotocol.Conflict{Code: sliceprotocol.ConflictSessionCandidateMissing, SourceID: sourceID})
			continue
		}
		if len(evidence.Candidates) != 1 {
			conflicts = append(conflicts, sliceprotocol.Conflict{Code: sliceprotocol.ConflictSessionCandidateAmbiguous, SourceID: sourceID})
			continue
		}
		name := evidence.Candidates[0]
		if !zellijlive.SafeSessionName(name) {
			conflicts = append(conflicts, sliceprotocol.Conflict{Code: sliceprotocol.ConflictSessionNameInvalid, SourceID: sourceID})
			continue
		}
		session := catalog.Exact(name)
		code, active := conflictForSession(session)
		if !active {
			conflicts = append(conflicts, sliceprotocol.Conflict{Code: code, SourceID: sourceID, SessionID: session.ID})
			continue
		}
		if window.WorkspaceID == nil {
			return nil, nil, fmt.Errorf("window %d missing workspace after Niri validation", window.ID)
		}
		workspace := workspaces[*window.WorkspaceID]
		key := keys[workspace.ID]
		if _, collision := colliding[key]; collision && key != "" {
			conflicts = append(conflicts, sliceprotocol.Conflict{Code: sliceprotocol.ConflictWorkspaceNameCollision, SourceID: sourceID, SessionID: session.ID})
			continue
		}
		var output *sliceprotocol.Output
		if workspace.Output != nil {
			observed := state.Outputs[*workspace.Output]
			output = &sliceprotocol.Output{Name: observed.Name, LogicalX: observed.Logical.X, LogicalY: observed.Logical.Y, LogicalWidth: observed.Logical.Width, LogicalHeight: observed.Logical.Height, Scale: observed.Logical.Scale, Transform: observed.Logical.Transform}
		}
		layout := sliceprotocol.Layout{TileWidth: window.Layout.TileSize[0], TileHeight: window.Layout.TileSize[1], WindowWidth: window.Layout.WindowSize[0], WindowHeight: window.Layout.WindowSize[1]}
		if window.IsFloating {
			layout.Mode = "floating"
		} else {
			layout.Mode = "tiled"
			layout.Position = &sliceprotocol.Position{Column: window.Layout.Position[0], Tile: window.Layout.Position[1]}
		}
		workspaceName := ""
		if workspace.Name != nil {
			workspaceName = *workspace.Name
		}
		source := sliceprotocol.Source{
			SourceID: sourceID, RuntimeWindowID: window.ID,
			Session:   sliceprotocol.Session{ID: session.ID, Name: session.Name, Status: "active"},
			Workspace: sliceprotocol.Workspace{RuntimeID: workspace.ID, Name: workspaceName, Key: key},
			Output:    output,
			Layout:    layout,
		}
		candidates = append(candidates, candidate{source: source, window: window, session: session})
	}
	counts := map[string]int{}
	for _, candidate := range candidates {
		counts[candidate.session.ID]++
	}
	sources := make([]sliceprotocol.Source, 0, len(candidates))
	for _, candidate := range candidates {
		if counts[candidate.session.ID] > 1 {
			conflicts = append(conflicts, sliceprotocol.Conflict{Code: sliceprotocol.ConflictSessionDuplicateBinding, SourceID: candidate.source.SourceID, SessionID: candidate.session.ID})
			continue
		}
		sources = append(sources, candidate.source)
	}
	authority := sliceprotocol.Canonicalize(sliceprotocol.Authoritative{WorkspaceNormalization: sliceprotocol.WorkspaceNormalization, Sources: sources, Conflicts: conflicts})
	return authority.Sources, authority.Conflicts, nil
}

func conflictForSession(session zellijlive.Session) (sliceprotocol.ConflictCode, bool) {
	switch session.Status {
	case zellijlive.StatusActive:
		return "", true
	case zellijlive.StatusDeadResurrectable:
		return sliceprotocol.ConflictSessionDeadResurrectable, false
	case zellijlive.StatusPrefixOnly:
		return sliceprotocol.ConflictSessionPrefixOnly, false
	case zellijlive.StatusDuplicate:
		return sliceprotocol.ConflictSessionCatalogDuplicate, false
	case zellijlive.StatusSocketInvalid:
		return sliceprotocol.ConflictSessionSocketInvalid, false
	default:
		return sliceprotocol.ConflictSessionMissing, false
	}
}
