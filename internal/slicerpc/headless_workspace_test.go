package slicerpc

import (
	"context"
	"testing"

	"github.com/jmo/terminal-redeemer/internal/niriipc"
)

type headlessWorkspaceNiri struct {
	state   niriipc.State
	actions []any
}

func (fake *headlessWorkspaceNiri) Snapshot(context.Context) (niriipc.State, error) {
	return fake.state, nil
}

func (fake *headlessWorkspaceNiri) Action(_ context.Context, action any) error {
	fake.actions = append(fake.actions, action)
	return nil
}

func headlessWorkspaceState(names ...string) niriipc.State {
	state := niriipc.State{Outputs: map[string]niriipc.Output{}}
	for index, name := range names {
		workspaceName := name
		state.Workspaces = append(state.Workspaces, niriipc.Workspace{ID: uint64(index + 1), Index: index + 1, Name: &workspaceName})
	}
	return state
}

func TestEnsureWorkspaceHeadlessReturnsOnlyExactExistingUniqueName(t *testing.T) {
	fake := &headlessWorkspaceNiri{state: headlessWorkspaceState("Work", "Chat")}
	server := Server{Niri: fake}
	id, err := server.EnsureWorkspace(context.Background(), "Work")
	if err != nil || id != 1 || len(fake.actions) != 0 {
		t.Fatalf("existing headless workspace: id=%d actions=%+v err=%v", id, fake.actions, err)
	}
}

func TestEnsureWorkspaceHeadlessMissingDuplicateAndCollisionFailWithoutCreation(t *testing.T) {
	cases := []struct {
		name      string
		requested string
		state     niriipc.State
	}{
		{name: "missing", requested: "Missing", state: headlessWorkspaceState("Work")},
		{name: "duplicate", requested: "Work", state: headlessWorkspaceState("Work", "Work")},
		{name: "normalization collision", requested: "Work", state: headlessWorkspaceState("Work", "ｗｏｒｋ")},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fake := &headlessWorkspaceNiri{state: test.state}
			if id, err := (Server{Niri: fake}).EnsureWorkspace(context.Background(), test.requested); err == nil || id != 0 {
				t.Fatalf("unsafe headless lookup accepted: id=%d err=%v", id, err)
			}
			if len(fake.actions) != 0 {
				t.Fatalf("headless failure attempted workspace creation: %+v", fake.actions)
			}
		})
	}
}
