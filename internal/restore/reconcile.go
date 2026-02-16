package restore

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/jmo/terminal-redeemer/internal/model"
)

type MoveRequest struct {
	WindowKey    string
	WindowID     int
	AppID        string
	WorkspaceRef string
}

type WindowMover interface {
	MoveToWorkspace(ctx context.Context, windowID int, workspaceRef string) error
}

func BuildMoveRequests(plan Plan, before model.State, after model.State) []MoveRequest {
	beforeKeys := make(map[string]struct{}, len(before.Windows))
	for _, window := range before.Windows {
		beforeKeys[window.Key] = struct{}{}
	}

	readyTargetsByApp := make(map[string][]string)
	for _, item := range plan.Items {
		if item.Status != StatusReady {
			continue
		}
		workspaceRef := strings.TrimSpace(item.WorkspaceID)
		if workspaceRef == "" {
			continue
		}
		appID := normalizeAppID(item.AppID)
		readyTargetsByApp[appID] = append(readyTargetsByApp[appID], workspaceRef)
	}

	newWindowsByApp := make(map[string][]model.Window)
	for _, window := range after.Windows {
		if _, existed := beforeKeys[window.Key]; existed {
			continue
		}
		appID := normalizeAppID(window.AppID)
		if _, tracked := readyTargetsByApp[appID]; !tracked {
			continue
		}
		newWindowsByApp[appID] = append(newWindowsByApp[appID], window)
	}

	for appID := range newWindowsByApp {
		sort.Slice(newWindowsByApp[appID], func(i, j int) bool {
			left := windowNumericID(newWindowsByApp[appID][i].Key)
			right := windowNumericID(newWindowsByApp[appID][j].Key)
			if left != right {
				return left < right
			}
			return newWindowsByApp[appID][i].Key < newWindowsByApp[appID][j].Key
		})
	}

	requests := make([]MoveRequest, 0)
	for appID, targets := range readyTargetsByApp {
		windows := newWindowsByApp[appID]
		for i := 0; i < len(targets) && i < len(windows); i++ {
			windowID := windowNumericID(windows[i].Key)
			if windowID <= 0 {
				continue
			}
			requests = append(requests, MoveRequest{
				WindowKey:    windows[i].Key,
				WindowID:     windowID,
				AppID:        appID,
				WorkspaceRef: targets[i],
			})
		}
	}

	sort.Slice(requests, func(i, j int) bool {
		if requests[i].WorkspaceRef != requests[j].WorkspaceRef {
			return requests[i].WorkspaceRef < requests[j].WorkspaceRef
		}
		if requests[i].AppID != requests[j].AppID {
			return requests[i].AppID < requests[j].AppID
		}
		return requests[i].WindowID < requests[j].WindowID
	})

	return requests
}

func ApplyMoveRequests(ctx context.Context, mover WindowMover, requests []MoveRequest) int {
	if mover == nil {
		return 0
	}
	applied := 0
	for _, request := range requests {
		if err := mover.MoveToWorkspace(ctx, request.WindowID, request.WorkspaceRef); err != nil {
			continue
		}
		applied++
	}
	return applied
}

func windowNumericID(windowKey string) int {
	parts := strings.Split(windowKey, ":")
	if len(parts) < 3 {
		return -1
	}
	id, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return -1
	}
	return id
}
