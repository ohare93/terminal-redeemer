package restore

import (
	"context"
	"errors"
	"testing"

	"github.com/jmo/terminal-redeemer/internal/model"
)

func TestPlanStatusClassification(t *testing.T) {
	t.Parallel()

	state := model.State{Windows: []model.Window{
		{Key: "w-term-ready", AppID: "kitty", Terminal: &model.Terminal{CWD: "/tmp", SessionTag: "sess-a"}},
		{Key: "w-term-missing", AppID: "kitty"},
		{Key: "w-app-skipped", AppID: "firefox"},
		{Key: "w-app-ready", AppID: "code"},
	}}

	planner := NewPlanner(PlannerConfig{
		AppAllowlist: map[string]string{"code": "code"},
		Terminal:     TerminalConfig{Command: "kitty", ZellijAttachOrCreate: true},
	})

	plan := planner.Build(state)
	if statusOf(plan, "w-term-ready") != StatusReady {
		t.Fatalf("expected w-term-ready to be ready")
	}
	if statusOf(plan, "w-term-missing") != StatusSkipped {
		t.Fatalf("expected w-term-missing to be skipped")
	}
	if statusOf(plan, "w-app-skipped") != StatusSkipped {
		t.Fatalf("expected w-app-skipped to be skipped")
	}
	if statusOf(plan, "w-app-ready") != StatusReady {
		t.Fatalf("expected w-app-ready to be ready")
	}
}

func TestTerminalRestoreSkipsMissingMetadata(t *testing.T) {
	t.Parallel()

	state := model.State{Windows: []model.Window{{Key: "w-1", AppID: "kitty"}}}
	planner := NewPlanner(PlannerConfig{Terminal: TerminalConfig{Command: "kitty", ZellijAttachOrCreate: true}})
	plan := planner.Build(state)

	if len(plan.Items) != 1 {
		t.Fatalf("expected one plan item, got %d", len(plan.Items))
	}
	if plan.Items[0].Status != StatusSkipped {
		t.Fatalf("expected skipped terminal with missing metadata, got %s", plan.Items[0].Status)
	}
}

func TestAppRestoreObeysAllowlistOnly(t *testing.T) {
	t.Parallel()

	state := model.State{Windows: []model.Window{{Key: "w-1", AppID: "firefox"}, {Key: "w-2", AppID: "code"}}}
	planner := NewPlanner(PlannerConfig{AppAllowlist: map[string]string{"code": "code"}, Terminal: TerminalConfig{Command: "kitty"}})
	plan := planner.Build(state)

	if statusOf(plan, "w-1") != StatusSkipped {
		t.Fatalf("expected firefox skipped")
	}
	if statusOf(plan, "w-2") != StatusReady {
		t.Fatalf("expected code ready")
	}
}

func TestExecutorContinueOnFailureSummary(t *testing.T) {
	t.Parallel()

	plan := Plan{Items: []Item{
		{WindowKey: "w-1", Status: StatusReady, Command: "ok-1"},
		{WindowKey: "w-2", Status: StatusReady, Command: "fail"},
		{WindowKey: "w-3", Status: StatusSkipped},
		{WindowKey: "w-4", Status: StatusReady, Command: "ok-2"},
	}}

	executor := NewExecutor(stubRunner{failCommand: "fail"})
	summary := executor.Execute(context.Background(), plan)

	if summary.Restored != 2 || summary.Skipped != 1 || summary.Failed != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
}

func statusOf(plan Plan, key string) Status {
	for _, item := range plan.Items {
		if item.WindowKey == key {
			return item.Status
		}
	}
	return ""
}

type stubRunner struct {
	failCommand string
}

func (s stubRunner) Run(_ context.Context, command string) error {
	if command == s.failCommand {
		return errors.New("boom")
	}
	return nil
}
