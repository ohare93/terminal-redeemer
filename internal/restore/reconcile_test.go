package restore

import (
	"context"
	"errors"
	"testing"

	"github.com/jmo/terminal-redeemer/internal/model"
)

func TestBuildMoveRequestsPerWindowAndOneshot(t *testing.T) {
	t.Parallel()

	plan := Plan{Items: []Item{
		{WindowKey: "saved-firefox-1", AppID: "firefox", WorkspaceID: "2", Status: StatusReady},
		{WindowKey: "saved-firefox-2", AppID: "firefox", WorkspaceID: "6", Status: StatusSkipped, Reason: "oneshot app already scheduled"},
		{WindowKey: "saved-kitty-1", AppID: "kitty", WorkspaceID: "5", Status: StatusReady},
		{WindowKey: "saved-kitty-2", AppID: "kitty", WorkspaceID: "11", Status: StatusReady},
	}}

	before := model.State{Windows: []model.Window{
		{Key: "w:firefox:7", AppID: "firefox"},
		{Key: "w:kitty:10", AppID: "kitty"},
	}}
	after := model.State{Windows: []model.Window{
		{Key: "w:firefox:7", AppID: "firefox"},
		{Key: "w:kitty:10", AppID: "kitty"},
		{Key: "w:firefox:20", AppID: "firefox"},
		{Key: "w:firefox:21", AppID: "firefox"},
		{Key: "w:kitty:30", AppID: "kitty"},
		{Key: "w:kitty:31", AppID: "kitty"},
	}}

	requests := BuildMoveRequests(plan, before, after)
	if len(requests) != 3 {
		t.Fatalf("expected 3 move requests, got %d: %#v", len(requests), requests)
	}

	type key struct {
		app string
		id  int
	}
	got := make(map[key]string, len(requests))
	for _, request := range requests {
		got[key{app: request.AppID, id: request.WindowID}] = request.WorkspaceRef
	}
	if got[key{app: "firefox", id: 20}] != "2" {
		t.Fatalf("missing firefox move request: %#v", got)
	}
	if got[key{app: "kitty", id: 30}] != "5" {
		t.Fatalf("missing kitty move request 30->5: %#v", got)
	}
	if got[key{app: "kitty", id: 31}] != "11" {
		t.Fatalf("missing kitty move request 31->11: %#v", got)
	}
}

func TestApplyMoveRequestsContinuesOnFailure(t *testing.T) {
	t.Parallel()

	requests := []MoveRequest{
		{WindowID: 1, WorkspaceRef: "2"},
		{WindowID: 2, WorkspaceRef: "5"},
		{WindowID: 3, WorkspaceRef: "6"},
	}

	mover := &stubWindowMover{failOn: map[int]error{2: errors.New("boom")}}
	report := ApplyMoveRequests(context.Background(), mover, requests)
	if report.Applied != 2 {
		t.Fatalf("expected 2 successful moves, got %d", report.Applied)
	}
	if report.Attempted != 3 {
		t.Fatalf("expected 3 attempted moves, got %d", report.Attempted)
	}
	if len(report.Failures) != 1 {
		t.Fatalf("expected one move failure, got %d", len(report.Failures))
	}
	if report.Failures[0].Request.WindowID != 2 {
		t.Fatalf("expected failure for window 2, got %#v", report.Failures[0].Request)
	}
	if report.Failures[0].Err == nil || report.Failures[0].Err.Error() != "boom" {
		t.Fatalf("expected failure error boom, got %v", report.Failures[0].Err)
	}
	if len(mover.calls) != 3 {
		t.Fatalf("expected all move requests attempted, got %d", len(mover.calls))
	}
}

type stubWindowMover struct {
	failOn map[int]error
	calls  []int
}

func (s *stubWindowMover) MoveToWorkspace(_ context.Context, windowID int, _ string) error {
	s.calls = append(s.calls, windowID)
	if err, ok := s.failOn[windowID]; ok {
		return err
	}
	return nil
}
