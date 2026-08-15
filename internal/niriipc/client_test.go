package niriipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmo/terminal-redeemer/internal/sliceprotocol"
)

func TestClientInitialReplayAndSeparateOutputs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "niri.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		request, _ := bufio.NewReader(conn).ReadString('\n')
		if request != "\"EventStream\"\n" {
			done <- errors.New("unexpected event request")
			return
		}
		_, err = conn.Write([]byte("{\"Ok\":\"Handled\"}\n" +
			"{\"WorkspacesChanged\":{\"workspaces\":[{\"id\":1,\"idx\":1,\"name\":\"dev\",\"output\":\"winit\",\"is_active\":true}]}}\n" +
			"{\"UnknownFutureEvent\":{\"value\":1}}\n" +
			"{\"WindowsChanged\":{\"windows\":[{\"id\":42,\"app_id\":\"kitty\",\"pid\":4242,\"workspace_id\":1,\"is_floating\":false,\"layout\":{\"pos_in_scrolling_layout\":[1,1],\"tile_size\":[900,700],\"window_size\":[900,700]}}]}}\n" +
			"{\"ConfigLoaded\":{\"failed\":false}}\n"))
		conn.Close()
		if err != nil {
			done <- err
			return
		}
		conn, err = listener.Accept()
		if err != nil {
			done <- err
			return
		}
		request, _ = bufio.NewReader(conn).ReadString('\n')
		if request != "\"Outputs\"\n" {
			done <- errors.New("unexpected outputs request")
			return
		}
		_, err = conn.Write([]byte("{\"Ok\":{\"Outputs\":{\"winit\":{\"name\":\"winit\",\"logical\":{\"x\":0,\"y\":0,\"width\":1920,\"height\":1080,\"scale\":1,\"transform\":\"Normal\"}}}}}\n"))
		conn.Close()
		done <- err
	}()
	state, err := (Client{SocketPath: path, Timeout: time.Second}).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Windows) != 1 || state.Windows[0].ID != 42 || state.Outputs["winit"].Logical.Width != 1920 {
		t.Fatalf("unexpected state: %+v", state)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestClientAcceptsCoherentZeroOutputReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "niri.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			done <- acceptErr
			return
		}
		_, _ = bufio.NewReader(conn).ReadString('\n')
		_, writeErr := conn.Write([]byte("{\"Ok\":\"Handled\"}\n" +
			"{\"WorkspacesChanged\":{\"workspaces\":[{\"id\":1,\"idx\":1,\"name\":\"dev\",\"output\":null}]}}\n" +
			"{\"WindowsChanged\":{\"windows\":[{\"id\":42,\"app_id\":\"kitty\",\"pid\":4242,\"workspace_id\":1,\"is_floating\":false,\"layout\":{\"pos_in_scrolling_layout\":[1,1],\"tile_size\":[900,700],\"window_size\":[900,700]}}]}}\n" +
			"{\"ConfigLoaded\":{\"failed\":false}}\n"))
		_ = conn.Close()
		if writeErr != nil {
			done <- writeErr
			return
		}
		conn, acceptErr = listener.Accept()
		if acceptErr != nil {
			done <- acceptErr
			return
		}
		_, _ = bufio.NewReader(conn).ReadString('\n')
		_, writeErr = conn.Write([]byte("{\"Ok\":{\"Outputs\":{}}}\n"))
		_ = conn.Close()
		done <- writeErr
	}()

	state, err := (Client{SocketPath: path, Timeout: time.Second}).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Outputs) != 0 || len(state.Workspaces) != 1 || state.Workspaces[0].Output != nil || len(state.Windows) != 1 {
		t.Fatalf("headless replay changed: %+v", state)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestClientTransientDanglingReplayConvergesBeforeConfigLoaded(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "niri-replay-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	path := filepath.Join(root, "niri.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			done <- acceptErr
			return
		}
		request, _ := bufio.NewReader(conn).ReadString('\n')
		if request != "\"EventStream\"\n" {
			done <- errors.New("unexpected event request")
			_ = conn.Close()
			return
		}
		// The first window snapshot is transiently inconsistent: workspace 99
		// does not exist. A later replacement converges to workspace 1 before
		// the successful replay barrier. Only that final replay state may escape.
		_, writeErr := conn.Write([]byte("{\"Ok\":\"Handled\"}\n" +
			"{\"WindowsChanged\":{\"windows\":[{\"id\":42,\"app_id\":\"kitty\",\"pid\":4242,\"workspace_id\":99,\"is_floating\":false,\"layout\":{\"pos_in_scrolling_layout\":[1,1],\"tile_size\":[900,700],\"window_size\":[900,700]}}]}}\n" +
			"{\"WorkspacesChanged\":{\"workspaces\":[{\"id\":1,\"idx\":1,\"name\":\"dev\",\"output\":\"winit\",\"is_active\":true}]}}\n" +
			"{\"WindowsChanged\":{\"windows\":[{\"id\":42,\"app_id\":\"kitty\",\"pid\":4242,\"workspace_id\":1,\"is_floating\":false,\"layout\":{\"pos_in_scrolling_layout\":[1,1],\"tile_size\":[900,700],\"window_size\":[900,700]}}]}}\n" +
			"{\"ConfigLoaded\":{\"failed\":false}}\n"))
		_ = conn.Close()
		if writeErr != nil {
			done <- writeErr
			return
		}
		conn, acceptErr = listener.Accept()
		if acceptErr != nil {
			done <- acceptErr
			return
		}
		request, _ = bufio.NewReader(conn).ReadString('\n')
		if request != "\"Outputs\"\n" {
			done <- errors.New("unexpected outputs request")
			_ = conn.Close()
			return
		}
		_, writeErr = conn.Write([]byte("{\"Ok\":{\"Outputs\":{\"winit\":{\"name\":\"winit\",\"logical\":{\"x\":0,\"y\":0,\"width\":1920,\"height\":1080,\"scale\":1,\"transform\":\"Normal\"}}}}}\n"))
		_ = conn.Close()
		done <- writeErr
	}()

	state, err := (Client{SocketPath: path, Timeout: time.Second}).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Windows) != 1 || state.Windows[0].WorkspaceID == nil || *state.Windows[0].WorkspaceID != 1 || len(state.Workspaces) != 1 || state.Workspaces[0].ID != 1 {
		t.Fatalf("transient dangling state escaped instead of final joined state: %+v", state)
	}
	if err := Validate(state); err != nil {
		t.Fatalf("published replay state is not final/authoritative: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestClientTimeoutIsTyped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "niri.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() { conn, _ := listener.Accept(); defer conn.Close(); time.Sleep(200 * time.Millisecond) }()
	_, err = (Client{SocketPath: path, Timeout: 30 * time.Millisecond}).Snapshot(context.Background())
	if err == nil || ReasonCode(err) != sliceprotocol.ReasonNiriReplayTimeout {
		t.Fatalf("unexpected error: %v code=%s", err, ReasonCode(err))
	}
}

func TestValidateRejectsDanglingGeometryAndTopology(t *testing.T) {
	outputName := "winit"
	workspaceID := uint64(1)
	base := State{Outputs: map[string]Output{"winit": {Name: "winit", Logical: Logical{Width: 100, Height: 100, Scale: 1}}}, Workspaces: []Workspace{{ID: 1, Output: &outputName, IsActive: true}}, Windows: []Window{{ID: 2, PID: 2, WorkspaceID: &workspaceID, Layout: Layout{Position: []int{1, 1}, TileSize: []float64{50, 50}, WindowSize: []int{50, 50}}}}}
	if err := Validate(base); err != nil {
		t.Fatal(err)
	}
	dangling := base
	missing := uint64(99)
	dangling.Windows = append([]Window(nil), base.Windows...)
	dangling.Windows[0].WorkspaceID = &missing
	if code := ReasonCode(Validate(dangling)); code != sliceprotocol.ReasonNiriMissingWorkspace {
		t.Fatalf("dangling code %s", code)
	}
	geometry := base
	geometry.Outputs = map[string]Output{"winit": {Name: "winit", Logical: Logical{Width: 0, Height: 100, Scale: 1}}}
	if code := ReasonCode(Validate(geometry)); code != sliceprotocol.ReasonNiriInvalidGeometry {
		t.Fatalf("geometry code %s", code)
	}
	headless := base
	headless.Outputs = map[string]Output{}
	headless.Workspaces = append([]Workspace(nil), base.Workspaces...)
	headless.Workspaces[0].Output = nil
	if err := Validate(headless); err != nil {
		t.Fatalf("coherent headless state rejected: %v", err)
	}
	partial := headless
	partial.Workspaces = append([]Workspace(nil), headless.Workspaces...)
	partial.Workspaces[0].Output = &outputName
	if code := ReasonCode(Validate(partial)); code != sliceprotocol.ReasonNiriMissingOutput {
		t.Fatalf("partial headless join code %s", code)
	}
	second := "other"
	topology := base
	topology.Outputs = map[string]Output{"winit": base.Outputs["winit"], "other": {Name: "other", Logical: Logical{Width: 100, Height: 100, Scale: 1}}}
	topology.Workspaces = append(append([]Workspace(nil), base.Workspaces...), Workspace{ID: 3, Output: &second, IsActive: true})
	if code := ReasonCode(Validate(topology)); code != sliceprotocol.ReasonNiriUnsupportedTopology {
		t.Fatalf("topology code %s", code)
	}
}

func TestInitialReplayFailureCodes(t *testing.T) {
	validPrefix := "{\"Ok\":\"Handled\"}\n{\"WorkspacesChanged\":{\"workspaces\":[]}}\n{\"WindowsChanged\":{\"windows\":[]}}\n"
	invalidUTF8 := append([]byte(validPrefix+"{\"FutureEvent\":{\"unknown\":\""), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte("\"}}\n")...)
	cases := []struct {
		name    string
		payload []byte
		max     int
		want    sliceprotocol.ReasonCode
	}{
		{"eof", []byte("{\"Ok\":\"Handled\"}\n{\"WorkspacesChanged\":{\"workspaces\":[]}}\n"), 1024, sliceprotocol.ReasonNiriReplayEOF},
		{"config failed", []byte(validPrefix + "{\"ConfigLoaded\":{\"failed\":true}}\n"), 1024, sliceprotocol.ReasonNiriConfigFailed},
		{"config null", []byte(validPrefix + "{\"ConfigLoaded\":null}\n"), 1024, sliceprotocol.ReasonNiriMalformed},
		{"config missing failed", []byte(validPrefix + "{\"ConfigLoaded\":{}}\n"), 1024, sliceprotocol.ReasonNiriMalformed},
		{"config failed wrong type", []byte(validPrefix + "{\"ConfigLoaded\":{\"failed\":\"false\"}}\n"), 1024, sliceprotocol.ReasonNiriMalformed},
		{"invalid UTF-8 in unknown event", invalidUTF8, 1024, sliceprotocol.ReasonNiriMalformed},
		{"oversize", []byte("{\"Ok\":\"Handled\"}\n{\"FutureEvent\":{\"padding\":\"abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz\"}}\n"), 40, sliceprotocol.ReasonNiriReplyTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "niri.sock")
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			go func() {
				conn, _ := listener.Accept()
				if conn == nil {
					return
				}
				defer conn.Close()
				_, _ = bufio.NewReader(conn).ReadString('\n')
				_, _ = conn.Write(tc.payload)
			}()
			_, err = (Client{SocketPath: path, Timeout: time.Second, MaxLineBytes: tc.max}).Snapshot(context.Background())
			if err == nil || ReasonCode(err) != tc.want {
				t.Fatalf("error=%v code=%s want=%s", err, ReasonCode(err), tc.want)
			}
		})
	}
}

func TestActionUsesExactDirectIPCAndHandledIsOnlyAcceptance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "niri.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	got := make(chan string, 1)
	go func() {
		conn, _ := listener.Accept()
		if conn == nil {
			return
		}
		defer conn.Close()
		line, _ := bufio.NewReader(conn).ReadString('\n')
		got <- line
		_, _ = conn.Write([]byte("{\"Ok\":\"Handled\"}\n"))
	}()
	action := map[string]any{"SetWorkspaceName": SetWorkspaceNameAction{Name: "hostile ' ; $()", Workspace: WorkspaceReference{ID: 9}}}
	if err := (Client{SocketPath: path, Timeout: time.Second}).Action(context.Background(), action); err != nil {
		t.Fatal(err)
	}
	want := "{\"Action\":{\"SetWorkspaceName\":{\"name\":\"hostile ' ; $()\",\"workspace\":{\"Id\":9}}}}\n"
	if line := <-got; line != want {
		t.Fatalf("line=%q want=%q", line, want)
	}
}

func TestSpatialActionTypesMarshalExactPinnedShapes(t *testing.T) {
	tests := []struct {
		action any
		want   string
	}{
		{map[string]any{"MoveWindowToWorkspace": MoveWindowToWorkspaceAction{WindowID: 42, Reference: WorkspaceReference{ID: 9}, Focus: false}}, `{"MoveWindowToWorkspace":{"window_id":42,"reference":{"Id":9},"focus":false}}`},
		{map[string]any{"MoveWindowToFloating": WindowIDAction{ID: 42}}, `{"MoveWindowToFloating":{"id":42}}`},
		{map[string]any{"MoveWindowToTiling": WindowIDAction{ID: 42}}, `{"MoveWindowToTiling":{"id":42}}`},
		{map[string]any{"SetWindowWidth": SetWindowSizeAction{ID: 42, Change: SetProportionChange{SetProportion: 45}}}, `{"SetWindowWidth":{"id":42,"change":{"SetProportion":45}}}`},
		{map[string]any{"SetWindowHeight": SetWindowSizeAction{ID: 42, Change: SetProportionChange{SetProportion: 40}}}, `{"SetWindowHeight":{"id":42,"change":{"SetProportion":40}}}`},
	}
	for _, test := range tests {
		payload, err := json.Marshal(test.action)
		if err != nil {
			t.Fatal(err)
		}
		if string(payload) != test.want {
			t.Fatalf("action=%s want=%s", payload, test.want)
		}
	}
}

func TestLockedNiriBinaryMatchesProductionVersionGate(t *testing.T) {
	if os.Getenv("RUN_LOCKED_NIRI_VERSION_CHECK") != "1" {
		t.Skip("locked Niri binary check not requested")
	}
	binary := os.Getenv("NIRI_BIN")
	expected := os.Getenv("EXPECTED_NIRI_VERSION")
	if binary == "" || expected == "" {
		t.Fatal("locked Niri check requires NIRI_BIN and EXPECTED_NIRI_VERSION")
	}
	if err := VerifyVersion(context.Background(), binary, expected); err != nil {
		t.Fatalf("locked Niri binary failed production version gate: %v", err)
	}
}

func TestVerifyVersionRequiresExactPinnedOutput(t *testing.T) {
	dir := t.TempDir()
	write := func(name, output string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '"+output+"'\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	good := SupportedVersionOutput
	if err := VerifyVersion(context.Background(), write("good", good), SupportedVersion); err != nil {
		t.Fatalf("rejected supported output %q: %v", good, err)
	}
	for _, output := range []string{
		"niri " + SupportedVersion,
		"niri 26.04.1",
		"niri 26.4",
		"niri 26.04evil",
		"evil " + good,
	} {
		if err := VerifyVersion(context.Background(), write(strings.ReplaceAll(output, " ", "-"), output), SupportedVersion); err == nil {
			t.Fatalf("accepted %q", output)
		}
	}
}
