package niriipc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jmo/terminal-redeemer/internal/sliceprotocol"
)

const (
	SupportedVersion       = "26.04"
	SupportedVersionOutput = "niri 26.04 (Nixpkgs)"
	DefaultMaxLineBytes    = 4 << 20
	DefaultMaxReplayBytes  = 16 << 20
	DefaultTimeout         = 5 * time.Second
)

type Client struct {
	SocketPath     string
	Timeout        time.Duration
	MaxLineBytes   int
	MaxReplayBytes int
	Dialer         *net.Dialer
}

func VerifyVersion(ctx context.Context, command, expected string) error {
	if strings.TrimSpace(command) == "" || expected != SupportedVersion {
		return fmt.Errorf("pinned Niri executable and expected version %s are required", SupportedVersion)
	}
	var output versionOutput
	cmd := exec.CommandContext(ctx, command, "--version")
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if version := strings.TrimSpace(output.String()); err != nil || version != SupportedVersionOutput {
		return fmt.Errorf("pinned Niri %s is unavailable", SupportedVersion)
	}
	return nil
}

type versionOutput struct{ bytes.Buffer }

func (output *versionOutput) Write(payload []byte) (int, error) {
	if output.Len()+len(payload) > 4096 {
		return 0, errors.New("version output exceeds bound")
	}
	return output.Buffer.Write(payload)
}

func (client Client) Snapshot(ctx context.Context) (State, error) {
	workspaces, windows, err := client.initialReplay(ctx)
	if err != nil {
		return State{}, err
	}
	outputs, err := client.outputs(ctx)
	if err != nil {
		return State{}, err
	}
	state := State{Outputs: outputs, Workspaces: workspaces, Windows: windows}
	if err := Validate(state); err != nil {
		return State{}, err
	}
	return state, nil
}

func (client Client) dial(ctx context.Context) (net.Conn, error) {
	if client.SocketPath == "" {
		return nil, reason(sliceprotocol.ReasonNiriSocketUnavailable, errors.New("socket path is required"))
	}
	dialer := client.Dialer
	if dialer == nil {
		dialer = &net.Dialer{}
	}
	conn, err := dialer.DialContext(ctx, "unix", client.SocketPath)
	if err != nil {
		return nil, reason(sliceprotocol.ReasonNiriSocketUnavailable, err)
	}
	deadline := time.Now().Add(client.timeout())
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return nil, reason(sliceprotocol.ReasonNiriSocketUnavailable, err)
	}
	return conn, nil
}

func (client Client) initialReplay(ctx context.Context) ([]Workspace, []Window, error) {
	conn, err := client.dial(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close()
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stopCancel()
	if _, err := io.WriteString(conn, "\"EventStream\"\n"); err != nil {
		return nil, nil, classifyReadError(err)
	}
	reader := bufio.NewReaderSize(conn, client.maxLine()+1)
	line, err := readLine(reader, client.maxLine())
	if err != nil {
		return nil, nil, classifyReadError(err)
	}
	var reply map[string]json.RawMessage
	if err := sliceprotocol.RejectDuplicateKeys(line); err != nil {
		return nil, nil, reason(sliceprotocol.ReasonNiriMalformed, err)
	}
	if err := json.Unmarshal(line, &reply); err != nil || string(reply["Ok"]) != `"Handled"` {
		return nil, nil, reason(sliceprotocol.ReasonNiriMalformed, errors.New("unexpected EventStream reply"))
	}
	total := len(line)
	var workspaces []Workspace
	var windows []Window
	sawWorkspaces, sawWindows := false, false
	for {
		line, err = readLine(reader, client.maxLine())
		if err != nil {
			return nil, nil, classifyReadError(err)
		}
		total += len(line)
		if total > client.maxReplay() {
			return nil, nil, reason(sliceprotocol.ReasonNiriReplyTooLarge, errors.New("initial replay too large"))
		}
		var event eventEnvelope
		if err := sliceprotocol.RejectDuplicateKeys(line); err != nil {
			return nil, nil, reason(sliceprotocol.ReasonNiriMalformed, err)
		}
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, nil, reason(sliceprotocol.ReasonNiriMalformed, err)
		}
		if raw, ok := event["WorkspacesChanged"]; ok {
			var payload struct {
				Workspaces []Workspace `json:"workspaces"`
			}
			if err := json.Unmarshal(raw, &payload); err != nil {
				return nil, nil, reason(sliceprotocol.ReasonNiriMalformed, err)
			}
			workspaces, sawWorkspaces = payload.Workspaces, true
		}
		if raw, ok := event["WindowsChanged"]; ok {
			var payload struct {
				Windows []Window `json:"windows"`
			}
			if err := json.Unmarshal(raw, &payload); err != nil {
				return nil, nil, reason(sliceprotocol.ReasonNiriMalformed, err)
			}
			windows, sawWindows = payload.Windows, true
		}
		if raw, ok := event["ConfigLoaded"]; ok {
			var payload struct {
				Failed *bool `json:"failed"`
			}
			if err := json.Unmarshal(raw, &payload); err != nil || payload.Failed == nil {
				return nil, nil, reason(sliceprotocol.ReasonNiriMalformed, errors.New("ConfigLoaded.failed must be an explicit boolean"))
			}
			if *payload.Failed {
				return nil, nil, reason(sliceprotocol.ReasonNiriConfigFailed, errors.New("Niri configuration failed"))
			}
			break
		}
	}
	if !sawWorkspaces || !sawWindows {
		return nil, nil, reason(sliceprotocol.ReasonNiriMalformed, errors.New("initial replay missing workspace or window snapshot"))
	}
	return workspaces, windows, nil
}

func (client Client) outputs(ctx context.Context) (map[string]Output, error) {
	conn, err := client.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stopCancel()
	request, _ := json.Marshal("Outputs")
	if _, err := conn.Write(append(request, '\n')); err != nil {
		return nil, classifyReadError(err)
	}
	reader := bufio.NewReaderSize(conn, client.maxLine()+1)
	line, err := readLine(reader, client.maxLine())
	if err != nil {
		return nil, classifyReadError(err)
	}
	if err := sliceprotocol.RejectDuplicateKeys(line); err != nil {
		return nil, reason(sliceprotocol.ReasonNiriMalformed, err)
	}
	var reply struct {
		Ok struct {
			Outputs map[string]Output `json:"Outputs"`
		} `json:"Ok"`
		Err string `json:"Err"`
	}
	if err := json.Unmarshal(line, &reply); err != nil {
		return nil, reason(sliceprotocol.ReasonNiriMalformed, err)
	}
	if reply.Err != "" || reply.Ok.Outputs == nil {
		return nil, reason(sliceprotocol.ReasonNiriMalformed, errors.New("unexpected Outputs reply"))
	}
	for name, output := range reply.Ok.Outputs {
		if output.Name == "" {
			output.Name = name
			reply.Ok.Outputs[name] = output
		}
	}
	return reply.Ok.Outputs, nil
}

// Action sends one typed direct-IPC action. Handled only means accepted; callers
// must verify the requested state with a subsequent Snapshot.
func (client Client) Action(ctx context.Context, action any) error {
	conn, err := client.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stopCancel()
	request, err := json.Marshal(map[string]any{"Action": action})
	if err != nil {
		return reason(sliceprotocol.ReasonNiriMalformed, err)
	}
	if len(request) > client.maxLine() {
		return reason(sliceprotocol.ReasonNiriReplyTooLarge, errors.New("Niri action too large"))
	}
	if _, err := conn.Write(append(request, '\n')); err != nil {
		return classifyReadError(err)
	}
	line, err := readLine(bufio.NewReaderSize(conn, client.maxLine()+1), client.maxLine())
	if err != nil {
		return classifyReadError(err)
	}
	if err := sliceprotocol.RejectDuplicateKeys(line); err != nil {
		return reason(sliceprotocol.ReasonNiriMalformed, err)
	}
	var reply map[string]json.RawMessage
	if err := json.Unmarshal(line, &reply); err != nil || string(reply["Ok"]) != `"Handled"` || reply["Err"] != nil {
		return reason(sliceprotocol.ReasonNiriMalformed, errors.New("Niri action was not handled"))
	}
	return nil
}

type SetWorkspaceNameAction struct {
	Name      string             `json:"name"`
	Workspace WorkspaceReference `json:"workspace"`
}
type WorkspaceReference struct {
	ID uint64 `json:"Id"`
}

type MoveWindowToWorkspaceAction struct {
	WindowID  uint64             `json:"window_id"`
	Reference WorkspaceReference `json:"reference"`
	Focus     bool               `json:"focus"`
}

type WindowIDAction struct {
	ID uint64 `json:"id"`
}

type SetWindowSizeAction struct {
	ID     uint64              `json:"id"`
	Change SetProportionChange `json:"change"`
}

type SetProportionChange struct {
	SetProportion float64 `json:"SetProportion"`
}

func readLine(reader *bufio.Reader, max int) ([]byte, error) {
	line, err := reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return nil, reason(sliceprotocol.ReasonNiriReplyTooLarge, errors.New("Niri reply line too large"))
	}
	if err != nil {
		return nil, err
	}
	if len(line) > max {
		return nil, reason(sliceprotocol.ReasonNiriReplyTooLarge, errors.New("Niri reply line too large"))
	}
	return line, nil
}

func classifyReadError(err error) error {
	var observation *ObservationError
	if errors.As(err, &observation) {
		return err
	}
	if errors.Is(err, io.EOF) {
		return reason(sliceprotocol.ReasonNiriReplayEOF, err)
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return reason(sliceprotocol.ReasonNiriReplayTimeout, err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return reason(sliceprotocol.ReasonNiriReplayTimeout, err)
	}
	return reason(sliceprotocol.ReasonNiriMalformed, fmt.Errorf("Niri IPC: %w", err))
}

func (client Client) timeout() time.Duration {
	if client.Timeout <= 0 {
		return DefaultTimeout
	}
	return client.Timeout
}
func (client Client) maxLine() int {
	if client.MaxLineBytes <= 0 {
		return DefaultMaxLineBytes
	}
	return client.MaxLineBytes
}
func (client Client) maxReplay() int {
	if client.MaxReplayBytes <= 0 {
		return DefaultMaxReplayBytes
	}
	return client.MaxReplayBytes
}
