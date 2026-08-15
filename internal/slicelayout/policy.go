package slicelayout

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/jmo/terminal-redeemer/internal/sliceprotocol"
)

type Side string

const (
	Host  Side = "host"
	Leech Side = "leech"
)

type ObservationQuality string

const (
	Complete ObservationQuality = "complete"
	Degraded ObservationQuality = "degraded"
)

type LayoutMode string

const (
	Tiled    LayoutMode = "tiled"
	Floating LayoutMode = "floating"
)

type PlanStatus string

const (
	PlanComplete PlanStatus = "complete"
	PlanDegraded PlanStatus = "degraded"
	PlanConflict PlanStatus = "conflict"
)

type ChangeKind string

const (
	ChangeInitialProjection ChangeKind = "initial_projection"
	ChangeEnsureWorkspace   ChangeKind = "ensure_workspace"
	ChangeWorkspace         ChangeKind = "workspace"
	ChangeLayoutMode        ChangeKind = "layout_mode"
	ChangeWidth             ChangeKind = "width_percent"
	ChangeHeight            ChangeKind = "height_percent"
)

type ConflictCode string

const (
	ConflictInvalidInput             ConflictCode = "invalid_input"
	ConflictWorkspaceDuplicate       ConflictCode = "workspace_duplicate"
	ConflictWorkspaceCollision       ConflictCode = "workspace_normalization_collision"
	ConflictOwnership                ConflictCode = "ownership_mismatch"
	ConflictWriteAwaitingVerify      ConflictCode = "write_awaiting_verification"
	ConflictOriginControllerMismatch ConflictCode = "origin_controller_mismatch"
	ConflictOriginGenerationMismatch ConflictCode = "origin_generation_mismatch"
	ConflictUnsupportedTopology      ConflictCode = "unsupported_topology"
	ConflictIncompleteObservation    ConflictCode = "incomplete_observation"
)

type Workspace struct {
	RuntimeID uint64 `json:"runtime_id"`
	Name      string `json:"name"`
	Key       string `json:"key"`
}

type Spatial struct {
	WorkspaceName string                  `json:"workspace_name"`
	WorkspaceKey  string                  `json:"workspace_key"`
	Mode          LayoutMode              `json:"mode"`
	WidthPercent  float64                 `json:"width_percent"`
	HeightPercent float64                 `json:"height_percent"`
	Order         *sliceprotocol.Position `json:"order,omitempty"`
}

type Observation struct {
	Quality         ObservationQuality      `json:"quality"`
	SourceID        string                  `json:"source_id"`
	SourceEpoch     string                  `json:"source_epoch"`
	RuntimeWindowID uint64                  `json:"runtime_window_id"`
	Output          sliceprotocol.Output    `json:"output"`
	Workspace       Workspace               `json:"workspace"`
	Mode            LayoutMode              `json:"mode"`
	WindowWidth     int                     `json:"window_width"`
	WindowHeight    int                     `json:"window_height"`
	Order           *sliceprotocol.Position `json:"order,omitempty"`
}

type Ownership struct {
	SourceID                  string `json:"source_id"`
	HostCompositorEpoch       string `json:"host_compositor_epoch"`
	LeechCompositorEpoch      string `json:"leech_compositor_epoch"`
	HostRuntimeWindowID       uint64 `json:"host_runtime_window_id"`
	LeechRuntimeWindowID      uint64 `json:"leech_runtime_window_id"`
	ProjectionPositivelyOwned bool   `json:"projection_positively_owned"`
}

type Origin struct {
	ControllerID string `json:"controller_id"`
	Generation   uint64 `json:"generation"`
	From         Side   `json:"from"`
	Mode         string `json:"mode"`
	Cause        string `json:"cause"`
}

type AppliedWrite struct {
	Origin Origin `json:"origin"`
	Target Side   `json:"target"`
}

type Change struct {
	Kind               ChangeKind `json:"kind"`
	WorkspaceRuntimeID uint64     `json:"workspace_runtime_id,omitempty"`
	WorkspaceName      string     `json:"workspace_name,omitempty"`
	WorkspaceKey       string     `json:"workspace_key,omitempty"`
	Mode               LayoutMode `json:"mode,omitempty"`
	Percent            float64    `json:"percent,omitempty"`
	WidthPercent       float64    `json:"width_percent,omitempty"`
	HeightPercent      float64    `json:"height_percent,omitempty"`
}

type Proposal struct {
	Target                Side     `json:"target"`
	TargetCompositorEpoch string   `json:"target_compositor_epoch,omitempty"`
	SourceID              string   `json:"source_id"`
	RuntimeWindowID       uint64   `json:"runtime_window_id,omitempty"`
	Origin                Origin   `json:"origin"`
	Changes               []Change `json:"changes"`
	VerifyAfterWrite      bool     `json:"verify_after_write"`
	Focus                 bool     `json:"focus"`
}

type Conflict struct {
	Code     ConflictCode `json:"code"`
	Property string       `json:"property,omitempty"`
	Detail   string       `json:"detail,omitempty"`
}

type Fidelity struct {
	Approximate bool                 `json:"approximate"`
	Reasons     []string             `json:"reasons"`
	Source      sliceprotocol.Output `json:"source"`
	Target      sliceprotocol.Output `json:"target"`
}

type Input struct {
	ControllerID    string        `json:"controller_id"`
	Generation      uint64        `json:"generation"`
	Host            Observation   `json:"host"`
	Leech           *Observation  `json:"leech,omitempty"`
	HostWorkspaces  []Workspace   `json:"host_workspaces"`
	LeechWorkspaces []Workspace   `json:"leech_workspaces"`
	Ownership       Ownership     `json:"ownership"`
	LastApplied     *AppliedWrite `json:"last_applied,omitempty"`
}

type Result struct {
	Status     PlanStatus   `json:"status"`
	Proposals  []Proposal   `json:"proposals"`
	Conflicts  []Conflict   `json:"conflicts"`
	Fidelity   Fidelity     `json:"fidelity"`
	OrderDrift []OrderDrift `json:"order_drift"`
}

func Plan(input Input) Result {
	result := Result{Status: PlanComplete}
	if input.ControllerID == "" || input.Generation == 0 {
		return conflictResult(result, ConflictInvalidInput, "origin", "controller identity and generation are required")
	}
	if input.Host.Quality != Complete || (input.Leech != nil && input.Leech.Quality != Complete) {
		result.Status = PlanDegraded
		result.Conflicts = append(result.Conflicts, Conflict{Code: ConflictIncompleteObservation, Detail: "incomplete observations never authorize spatial writes"})
		return result
	}
	if mismatch := pendingOriginMismatch(input.LastApplied, input.ControllerID, input.Generation, Leech); mismatch != nil {
		result.Status = PlanConflict
		result.Conflicts = append(result.Conflicts, *mismatch)
		return result
	}
	hostSpatial, err := spatial(input.Host, true)
	if err != nil {
		return conflictResult(result, ConflictInvalidInput, "host", err.Error())
	}
	if conflicts := ValidateWorkspaceCatalog(input.HostWorkspaces); len(conflicts) != 0 {
		result.Status, result.Conflicts = PlanConflict, append(result.Conflicts, conflicts...)
		return result
	}
	if conflicts := ValidateWorkspaceCatalog(input.LeechWorkspaces); len(conflicts) != 0 {
		result.Status, result.Conflicts = PlanConflict, append(result.Conflicts, conflicts...)
		return result
	}
	if input.Leech == nil {
		result.Fidelity = fidelity(input.Host.Output, sliceprotocol.Output{})
		p := proposal(input, 0, []Change{{Kind: ChangeInitialProjection, WorkspaceName: hostSpatial.WorkspaceName, WorkspaceKey: hostSpatial.WorkspaceKey, Mode: hostSpatial.Mode, WidthPercent: hostSpatial.WidthPercent, HeightPercent: hostSpatial.HeightPercent}}, "initial_projection")
		appendProposal(&result, input.LastApplied, p)
		return result
	}
	leechSpatial, err := spatial(*input.Leech, false)
	if err != nil {
		return conflictResult(result, ConflictInvalidInput, "leech", err.Error())
	}
	result.Fidelity = fidelity(input.Host.Output, input.Leech.Output)
	if !owns(input) {
		return conflictResult(result, ConflictOwnership, "source_id", "exact host source and owned projection runtime IDs are required")
	}
	result.OrderDrift = CompareOrder([]OrderItem{{SourceID: input.Host.SourceID, Position: hostSpatial.Order}}, []OrderItem{{SourceID: input.Leech.SourceID, Position: leechSpatial.Order}})

	changes, conflicts := changesForTarget(hostSpatial, leechSpatial, input.LeechWorkspaces)
	result.Conflicts = append(result.Conflicts, conflicts...)
	if len(changes) != 0 {
		p := proposal(input, input.Leech.RuntimeWindowID, changes, "host_authority")
		appendProposal(&result, input.LastApplied, p)
	}
	if len(result.Conflicts) != 0 {
		result.Status = PlanConflict
	}
	return result
}

func spatial(observation Observation, allowMissingOutput bool) (Spatial, error) {
	if observation.SourceID == "" || observation.SourceEpoch == "" || observation.RuntimeWindowID == 0 {
		return Spatial{}, errors.New("opaque source, epoch, and same-epoch runtime window ID are required")
	}
	hasOutput := observation.Output.LogicalWidth != 0 || observation.Output.LogicalHeight != 0 || observation.Output.Scale != 0 || observation.Output.Name != "" || observation.Output.Transform != "" || observation.Output.LogicalX != 0 || observation.Output.LogicalY != 0
	if !hasOutput && !allowMissingOutput {
		return Spatial{}, errors.New("target output geometry is required")
	}
	if hasOutput && (observation.Output.LogicalWidth <= 0 || observation.Output.LogicalHeight <= 0 || observation.Output.Scale <= 0 || math.IsNaN(observation.Output.Scale) || math.IsInf(observation.Output.Scale, 0)) {
		return Spatial{}, errors.New("output geometry must be wholly absent or valid")
	}
	if observation.WindowWidth <= 0 || observation.WindowHeight <= 0 {
		return Spatial{}, errors.New("positive observed window dimensions are required")
	}
	key, err := sliceprotocol.NormalizeWorkspaceName(observation.Workspace.Name)
	if err != nil || observation.Workspace.Key != key {
		return Spatial{}, errors.New("workspace name and canonical key do not match")
	}
	if observation.Mode != Tiled && observation.Mode != Floating {
		return Spatial{}, errors.New("unknown layout mode")
	}
	if observation.Mode == Tiled {
		if observation.Order == nil || observation.Order.Column <= 0 || observation.Order.Tile <= 0 {
			return Spatial{}, errors.New("tiled order must be one-based")
		}
	} else if observation.Order != nil {
		return Spatial{}, errors.New("floating windows do not carry tiled order")
	}
	width, height := 0.0, 0.0
	if hasOutput {
		width = clamp(float64(observation.WindowWidth) / float64(observation.Output.LogicalWidth) * 100)
		height = clamp(float64(observation.WindowHeight) / float64(observation.Output.LogicalHeight) * 100)
	}
	return Spatial{WorkspaceName: observation.Workspace.Name, WorkspaceKey: key, Mode: observation.Mode, WidthPercent: width, HeightPercent: height, Order: copyPosition(observation.Order)}, nil
}

func clamp(value float64) float64 {
	if value < 1 {
		return 1
	}
	if value > 100 {
		return 100
	}
	return math.Round(value*10000) / 10000
}

func owns(input Input) bool {
	return input.Ownership.ProjectionPositivelyOwned &&
		input.Ownership.SourceID == input.Host.SourceID &&
		input.Leech.SourceID == input.Host.SourceID &&
		input.Ownership.HostCompositorEpoch != "" &&
		input.Ownership.LeechCompositorEpoch != "" &&
		input.Ownership.HostCompositorEpoch == input.Host.SourceEpoch &&
		input.Ownership.LeechCompositorEpoch == input.Leech.SourceEpoch &&
		input.Ownership.HostRuntimeWindowID == input.Host.RuntimeWindowID &&
		input.Ownership.LeechRuntimeWindowID == input.Leech.RuntimeWindowID
}

func proposal(input Input, runtimeID uint64, changes []Change, cause string) Proposal {
	targetEpoch := ""
	if input.Leech != nil {
		targetEpoch = input.Leech.SourceEpoch
	}
	return Proposal{Target: Leech, TargetCompositorEpoch: targetEpoch, SourceID: input.Host.SourceID, RuntimeWindowID: runtimeID, Origin: Origin{ControllerID: input.ControllerID, Generation: input.Generation, From: Host, Mode: "host_location", Cause: cause}, Changes: changes, VerifyAfterWrite: true, Focus: false}
}

func pendingOriginMismatch(last *AppliedWrite, controllerID string, generation uint64, target Side) *Conflict {
	if last == nil || last.Target != target {
		return nil
	}
	if last.Origin.ControllerID != controllerID {
		return &Conflict{Code: ConflictOriginControllerMismatch, Property: "origin", Detail: "pending write controller does not match the current controller"}
	}
	if last.Origin.Generation != generation {
		return &Conflict{Code: ConflictOriginGenerationMismatch, Property: "origin", Detail: "pending write generation does not match the current generation"}
	}
	return nil
}

func appendProposal(result *Result, last *AppliedWrite, proposal Proposal) {
	if mismatch := pendingOriginMismatch(last, proposal.Origin.ControllerID, proposal.Origin.Generation, proposal.Target); mismatch != nil {
		result.Status = PlanConflict
		result.Conflicts = append(result.Conflicts, *mismatch)
		return
	}
	if last != nil && last.Target == proposal.Target {
		// Suppression is keyed exactly by controller, generation, and target.
		// From/mode/cause are diagnostic and cannot bypass the pending gate.
		result.Status = PlanConflict
		result.Conflicts = append(result.Conflicts, Conflict{Code: ConflictWriteAwaitingVerify, Property: "origin", Detail: "the same controller/generation/target write is awaiting verification"})
		return
	}
	result.Proposals = append(result.Proposals, proposal)
}

func changesForTarget(desired, current Spatial, catalog []Workspace) ([]Change, []Conflict) {
	changes := make([]Change, 0, 4)
	var conflicts []Conflict
	if desired.WorkspaceKey != current.WorkspaceKey {
		workspace, found := workspaceByKey(catalog, desired.WorkspaceKey)
		if !found {
			changes = append(changes, Change{Kind: ChangeEnsureWorkspace, WorkspaceName: desired.WorkspaceName, WorkspaceKey: desired.WorkspaceKey})
			return changes, conflicts
		}
		changes = append(changes, Change{Kind: ChangeWorkspace, WorkspaceRuntimeID: workspace.RuntimeID, WorkspaceName: workspace.Name, WorkspaceKey: workspace.Key})
	}
	if desired.Mode != current.Mode {
		changes = append(changes, Change{Kind: ChangeLayoutMode, Mode: desired.Mode})
	}
	if desired.WidthPercent > 0 && !near(desired.WidthPercent, current.WidthPercent) {
		changes = append(changes, Change{Kind: ChangeWidth, Percent: desired.WidthPercent})
	}
	if desired.HeightPercent > 0 && !near(desired.HeightPercent, current.HeightPercent) {
		changes = append(changes, Change{Kind: ChangeHeight, Percent: desired.HeightPercent})
	}
	return changes, conflicts
}

func workspaceByKey(catalog []Workspace, key string) (Workspace, bool) {
	for _, workspace := range catalog {
		if workspace.Key == key {
			return workspace, true
		}
	}
	return Workspace{}, false
}

func ValidateWorkspaceCatalog(catalog []Workspace) []Conflict {
	seen := map[string]Workspace{}
	var conflicts []Conflict
	for _, workspace := range catalog {
		if workspace.RuntimeID == 0 || strings.TrimSpace(workspace.Name) == "" {
			conflicts = append(conflicts, Conflict{Code: ConflictInvalidInput, Property: "workspace", Detail: "named workspace requires a positive same-epoch runtime ID"})
			continue
		}
		key, err := sliceprotocol.NormalizeWorkspaceName(workspace.Name)
		if err != nil || key != workspace.Key {
			conflicts = append(conflicts, Conflict{Code: ConflictInvalidInput, Property: "workspace", Detail: "workspace name/key mismatch"})
			continue
		}
		if prior, ok := seen[key]; ok {
			code := ConflictWorkspaceCollision
			if prior.Name == workspace.Name {
				code = ConflictWorkspaceDuplicate
			}
			conflicts = append(conflicts, Conflict{Code: code, Property: "workspace", Detail: key})
			continue
		}
		seen[key] = workspace
	}
	return conflicts
}

func near(a, b float64) bool { return math.Abs(a-b) <= 0.01 }

func fidelity(source, target sliceprotocol.Output) Fidelity {
	reasons := []string{"niri_working_area_unobservable", "terminal_cell_grid_may_differ"}
	if target.LogicalWidth > 0 && (source.LogicalWidth != target.LogicalWidth || source.LogicalHeight != target.LogicalHeight) {
		reasons = append(reasons, "logical_dimensions_differ")
	}
	if target.Scale > 0 && source.Scale != target.Scale {
		reasons = append(reasons, "output_scale_differs")
	}
	return Fidelity{Approximate: true, Reasons: reasons, Source: source, Target: target}
}

func conflictResult(result Result, code ConflictCode, property, detail string) Result {
	result.Status = PlanConflict
	result.Conflicts = append(result.Conflicts, Conflict{Code: code, Property: property, Detail: detail})
	return result
}

type OrderItem struct {
	SourceID string                  `json:"source_id"`
	Position *sliceprotocol.Position `json:"position,omitempty"`
}

type OrderDrift struct {
	SourceID string                  `json:"source_id"`
	Expected *sliceprotocol.Position `json:"expected,omitempty"`
	Observed *sliceprotocol.Position `json:"observed,omitempty"`
}

func InitialLaunchOrder(items []OrderItem) []OrderItem {
	ordered := append([]OrderItem(nil), items...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.Position == nil || b.Position == nil {
			if a.Position == nil && b.Position == nil {
				return a.SourceID < b.SourceID
			}
			return a.Position != nil
		}
		if a.Position.Column != b.Position.Column {
			return a.Position.Column < b.Position.Column
		}
		if a.Position.Tile != b.Position.Tile {
			return a.Position.Tile < b.Position.Tile
		}
		return a.SourceID < b.SourceID
	})
	return ordered
}

func CompareOrder(expected, observed []OrderItem) []OrderDrift {
	observedByID := make(map[string]*sliceprotocol.Position, len(observed))
	for _, item := range observed {
		observedByID[item.SourceID] = item.Position
	}
	var drift []OrderDrift
	for _, item := range expected {
		actual, found := observedByID[item.SourceID]
		if !found || !equalPosition(item.Position, actual) {
			drift = append(drift, OrderDrift{SourceID: item.SourceID, Expected: copyPosition(item.Position), Observed: copyPosition(actual)})
		}
	}
	sort.Slice(drift, func(i, j int) bool { return drift[i].SourceID < drift[j].SourceID })
	return drift
}

func equalPosition(a, b *sliceprotocol.Position) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && a.Column == b.Column && a.Tile == b.Tile)
}

func copyPosition(position *sliceprotocol.Position) *sliceprotocol.Position {
	if position == nil {
		return nil
	}
	copy := *position
	return &copy
}

func (proposal Proposal) ValidateNonDisruptive() error {
	if proposal.Target != Leech {
		return errors.New("spatial proposals must target the leech projection")
	}
	if proposal.SourceID == "" || proposal.Origin.ControllerID == "" || proposal.Origin.Generation == 0 || proposal.Origin.From != Host || proposal.Origin.Mode != "host_location" || !proposal.VerifyAfterWrite || proposal.Focus {
		return errors.New("proposal lacks non-disruptive origin/verification invariants")
	}
	if proposal.RuntimeWindowID != 0 && proposal.TargetCompositorEpoch == "" {
		return errors.New("exact-window proposal lacks its target compositor epoch")
	}
	for _, change := range proposal.Changes {
		switch change.Kind {
		case ChangeInitialProjection, ChangeEnsureWorkspace:
		case ChangeWorkspace, ChangeLayoutMode, ChangeWidth, ChangeHeight:
			if proposal.RuntimeWindowID == 0 {
				return fmt.Errorf("%s requires an exact same-epoch runtime window ID", change.Kind)
			}
		default:
			return errors.New("unknown change kind")
		}
	}
	return nil
}
