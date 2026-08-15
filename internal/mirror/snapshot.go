package mirror

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jmo/terminal-redeemer/internal/model"
	"github.com/jmo/terminal-redeemer/internal/procmeta"
	"github.com/jmo/terminal-redeemer/internal/zellijlive"
)

const DefaultNiriCommand = "niri msg -j windows"

type CommandRunner interface {
	Run(ctx context.Context, command string) ([]byte, error)
}

type SessionLister interface {
	List(context.Context) ([]string, error)
}

type liveSessionLister struct {
	cataloger zellijlive.Cataloger
}

func (l liveSessionLister) List(ctx context.Context) ([]string, error) {
	catalog, err := l.cataloger.Observe(ctx)
	if err != nil {
		return nil, err
	}
	active := make([]string, 0, len(catalog.Names))
	for _, name := range catalog.Names {
		if catalog.Exact(name).Status == zellijlive.StatusActive {
			active = append(active, name)
		}
	}
	return active, nil
}

type Options struct {
	Host            string
	Profile         string
	NiriCommand     string
	FixturePath     string
	ProcessMetadata procmeta.Config
	GeneratedAt     time.Time
	Runner          CommandRunner
	Reader          procmeta.Reader
	Verifier        procmeta.SessionVerifier
	Resolver        procmeta.SessionCWDResolver
	Lister          SessionLister
}

type Snapshot struct {
	Host        string    `json:"host"`
	Profile     string    `json:"profile"`
	GeneratedAt time.Time `json:"generated_at"`
	Windows     []Window  `json:"windows"`
}

type Window struct {
	Order          int       `json:"order"`
	SourceWindowID int       `json:"source_window_id"`
	Key            string    `json:"key"`
	AppID          string    `json:"app_id"`
	Title          string    `json:"title,omitempty"`
	PID            int       `json:"pid,omitempty"`
	WorkspaceID    string    `json:"workspace_id,omitempty"`
	WorkspaceIndex int       `json:"workspace_index,omitempty"`
	WorkspaceName  string    `json:"workspace_name,omitempty"`
	Output         string    `json:"output,omitempty"`
	Position       []float64 `json:"position,omitempty"`
	TileSize       []float64 `json:"tile_size,omitempty"`
	WindowSize     []int     `json:"window_size,omitempty"`
	IsFocused      bool      `json:"is_focused,omitempty"`
	IsFloating     bool      `json:"is_floating,omitempty"`
	Headless       bool      `json:"headless,omitempty"`
	ZellijSession  string    `json:"zellij_session,omitempty"`
	Terminal       *Terminal `json:"terminal,omitempty"`
}

type Terminal struct {
	CWD           string   `json:"cwd,omitempty"`
	ProcessTags   []string `json:"process_tags,omitempty"`
	ZellijSession string   `json:"zellij_session,omitempty"`
}

type payload struct {
	Workspaces []workspacePayload `json:"workspaces"`
	Windows    []windowPayload    `json:"windows"`
}

type workspacePayload struct {
	ID     any    `json:"id"`
	Index  int    `json:"idx"`
	Name   any    `json:"name"`
	Output string `json:"output"`
}

type windowPayload struct {
	ID          int           `json:"id"`
	AppID       any           `json:"app_id"`
	Title       string        `json:"title"`
	WorkspaceID any           `json:"workspace_id"`
	PID         int           `json:"pid"`
	IsFocused   bool          `json:"is_focused"`
	IsFloating  bool          `json:"is_floating"`
	Layout      layoutPayload `json:"layout"`
}

type layoutPayload struct {
	PosInScrollingLayout []float64 `json:"pos_in_scrolling_layout"`
	TileSize             []float64 `json:"tile_size"`
	WindowSize           []int     `json:"window_size"`
}

type workspaceRef struct {
	ID     string
	Index  int
	Name   string
	Output string
}

type orderedWindow struct {
	window Window
	raw    windowPayload
}

func Capture(ctx context.Context, opts Options) (Snapshot, error) {
	if strings.TrimSpace(opts.Host) == "" {
		opts.Host = "local"
	}
	if strings.TrimSpace(opts.Profile) == "" {
		opts.Profile = "default"
	}
	if opts.GeneratedAt.IsZero() {
		opts.GeneratedAt = time.Now().UTC()
	}

	raw, err := readSnapshotPayload(ctx, opts)
	if err != nil {
		return Snapshot{}, err
	}

	parsed, err := parsePayload(raw)
	if err != nil {
		return Snapshot{}, err
	}

	reader := opts.Reader
	if reader == nil {
		reader = procmeta.ProcReader{}
	}
	verifier := opts.Verifier
	if verifier == nil {
		verifier = procmeta.NewZellijSessionVerifier(nil)
	}
	resolver := opts.Resolver
	if resolver == nil {
		resolver = procmeta.NewZellijSessionCWDResolver("")
	}
	enricher := procmeta.NewEnricherWithDependencies(reader, opts.ProcessMetadata, verifier, resolver)

	workspaces := workspaceRefs(parsed.Workspaces)
	ordered := make([]orderedWindow, 0, len(parsed.Windows))
	for _, rawWindow := range parsed.Windows {
		appID, _ := valueAsString(rawWindow.AppID)
		workspaceID, _ := valueAsString(rawWindow.WorkspaceID)
		ref := workspaces[workspaceID]
		modelWindow := model.Window{
			Key:         fmt.Sprintf("w:%s:%d", appID, rawWindow.ID),
			AppID:       appID,
			WorkspaceID: workspaceID,
			PID:         rawWindow.PID,
			Title:       rawWindow.Title,
		}
		enriched, err := enricher.EnrichWindow(modelWindow)
		if err != nil {
			return Snapshot{}, fmt.Errorf("enrich window %s: %w", modelWindow.Key, err)
		}

		out := Window{
			SourceWindowID: rawWindow.ID,
			Key:            modelWindow.Key,
			AppID:          appID,
			Title:          rawWindow.Title,
			PID:            rawWindow.PID,
			WorkspaceID:    workspaceID,
			WorkspaceIndex: ref.Index,
			WorkspaceName:  ref.Name,
			Output:         ref.Output,
			Position:       copyFloatSlice(rawWindow.Layout.PosInScrollingLayout),
			TileSize:       copyFloatSlice(rawWindow.Layout.TileSize),
			WindowSize:     copyIntSlice(rawWindow.Layout.WindowSize),
			IsFocused:      rawWindow.IsFocused,
			IsFloating:     rawWindow.IsFloating,
		}
		if enriched.Terminal != nil {
			terminal := &Terminal{
				CWD:           enriched.Terminal.CWD,
				ProcessTags:   append([]string(nil), enriched.Terminal.ProcessTags...),
				ZellijSession: enriched.Terminal.SessionTag,
			}
			out.Terminal = terminal
			out.ZellijSession = terminal.ZellijSession
		}
		ordered = append(ordered, orderedWindow{window: out, raw: rawWindow})
	}

	sort.SliceStable(ordered, func(i, j int) bool {
		return lessWindow(ordered[i], ordered[j])
	})

	windows := make([]Window, 0, len(ordered))
	visibleSessions := make(map[string]struct{})
	for i, entry := range ordered {
		entry.window.Order = i
		windows = append(windows, entry.window)
		if session := SessionName(entry.window); session != "" {
			visibleSessions[session] = struct{}{}
		}
	}

	lister := opts.Lister
	if lister == nil && strings.TrimSpace(opts.FixturePath) == "" {
		lister = liveSessionLister{cataloger: zellijlive.CommandCataloger{}}
	}
	if lister != nil {
		sessions, err := lister.List(ctx)
		if err != nil {
			return Snapshot{}, fmt.Errorf("list live Zellij sessions: %w", err)
		}
		sort.Strings(sessions)
		added := make(map[string]struct{}, len(sessions))
		for _, session := range sessions {
			session = strings.TrimSpace(session)
			if session == "" {
				continue
			}
			if _, found := visibleSessions[session]; found {
				continue
			}
			if _, found := added[session]; found {
				continue
			}
			added[session] = struct{}{}
			cwd, _ := resolver.Resolve(session)
			windows = append(windows, Window{
				Order:         len(windows),
				Key:           "zellij:" + session,
				AppID:         "zellij",
				Title:         session,
				Headless:      true,
				ZellijSession: session,
				Terminal:      &Terminal{CWD: strings.TrimSpace(cwd), ZellijSession: session},
			})
		}
	}

	return Snapshot{
		Host:        opts.Host,
		Profile:     opts.Profile,
		GeneratedAt: opts.GeneratedAt,
		Windows:     windows,
	}, nil
}

func readSnapshotPayload(ctx context.Context, opts Options) ([]byte, error) {
	if strings.TrimSpace(opts.FixturePath) != "" {
		payload, err := os.ReadFile(opts.FixturePath)
		if err != nil {
			return nil, fmt.Errorf("read mirror fixture: %w", err)
		}
		return payload, nil
	}

	command := strings.TrimSpace(opts.NiriCommand)
	if command == "" {
		command = DefaultNiriCommand
	}
	runner := opts.Runner
	if runner == nil {
		runner = ShellRunner{}
	}

	windows, err := runner.Run(ctx, command)
	if err != nil {
		return nil, fmt.Errorf("run niri mirror command: %w", err)
	}
	if command != DefaultNiriCommand {
		return windows, nil
	}

	workspaces, err := runner.Run(ctx, "niri msg -j workspaces")
	if err != nil {
		return windows, nil
	}
	combined, err := combinePayloads(workspaces, windows)
	if err != nil {
		return windows, nil
	}
	return combined, nil
}

func parsePayload(raw []byte) (payload, error) {
	var parsed payload
	if err := json.Unmarshal(raw, &parsed); err != nil {
		var windowsOnly []windowPayload
		if windowsErr := json.Unmarshal(raw, &windowsOnly); windowsErr != nil {
			return payload{}, fmt.Errorf("decode mirror snapshot: %w", err)
		}
		parsed = payload{Windows: windowsOnly}
	}
	return parsed, nil
}

func combinePayloads(workspaces []byte, windows []byte) ([]byte, error) {
	var workspacesPayload []any
	if err := json.Unmarshal(workspaces, &workspacesPayload); err != nil {
		return nil, err
	}
	var windowsPayload []any
	if err := json.Unmarshal(windows, &windowsPayload); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"workspaces": workspacesPayload,
		"windows":    windowsPayload,
	})
}

func workspaceRefs(workspaces []workspacePayload) map[string]workspaceRef {
	refs := make(map[string]workspaceRef, len(workspaces))
	for _, workspace := range workspaces {
		id, _ := valueAsString(workspace.ID)
		if id == "" {
			continue
		}
		name, _ := valueAsString(workspace.Name)
		refs[id] = workspaceRef{ID: id, Index: workspace.Index, Name: name, Output: workspace.Output}
	}
	return refs
}

func lessWindow(left orderedWindow, right orderedWindow) bool {
	leftWorkspace := sortWorkspaceIndex(left.window.WorkspaceIndex)
	rightWorkspace := sortWorkspaceIndex(right.window.WorkspaceIndex)
	if leftWorkspace != rightWorkspace {
		return leftWorkspace < rightWorkspace
	}

	leftX, leftY := sortPosition(left.raw.Layout.PosInScrollingLayout)
	rightX, rightY := sortPosition(right.raw.Layout.PosInScrollingLayout)
	if leftX != rightX {
		return leftX < rightX
	}
	if leftY != rightY {
		return leftY < rightY
	}
	if left.window.SourceWindowID != right.window.SourceWindowID {
		return left.window.SourceWindowID < right.window.SourceWindowID
	}
	return left.window.Key < right.window.Key
}

func sortWorkspaceIndex(index int) int {
	if index > 0 {
		return index
	}
	return int(^uint(0) >> 1)
}

func sortPosition(pos []float64) (float64, float64) {
	const missing = 1 << 30
	if len(pos) == 0 {
		return missing, missing
	}
	if len(pos) == 1 {
		return pos[0], missing
	}
	return pos[0], pos[1]
}

func valueAsString(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", false
	case string:
		return x, x != ""
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case int:
		return strconv.Itoa(x), true
	default:
		return fmt.Sprint(x), true
	}
}

func copyFloatSlice(in []float64) []float64 {
	if len(in) == 0 {
		return nil
	}
	return append([]float64(nil), in...)
}

func copyIntSlice(in []int) []int {
	if len(in) == 0 {
		return nil
	}
	return append([]int(nil), in...)
}

type ShellRunner struct{}

func (ShellRunner) Run(ctx context.Context, command string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "sh", "-lc", command)
	cmd.Env = envWithGraphicalSession(os.Environ())
	return cmd.Output()
}

func envWithGraphicalSession(env []string) []string {
	if hasEnv(env, "NIRI_SOCKET") {
		return env
	}

	out, err := exec.Command("systemctl", "--user", "show-environment").Output()
	if err != nil {
		return env
	}

	merged := append([]string(nil), env...)
	for _, line := range strings.Split(string(out), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		switch name {
		case "NIRI_SOCKET", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR":
			merged = setEnv(merged, name, value)
		}
	}
	return merged
}

func hasEnv(env []string, name string) bool {
	prefix := name + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) && strings.TrimSpace(strings.TrimPrefix(entry, prefix)) != "" {
			return true
		}
	}
	return false
}

func setEnv(env []string, name string, value string) []string {
	prefix := name + "="
	entry := prefix + value
	for i := range env {
		if strings.HasPrefix(env[i], prefix) {
			env[i] = entry
			return env
		}
	}
	return append(env, entry)
}
