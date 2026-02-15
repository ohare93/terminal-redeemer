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
		{Key: "w-term-degraded", AppID: "kitty", Terminal: &model.Terminal{CWD: "/tmp"}},
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
	if statusOf(plan, "w-term-degraded") != StatusDegraded {
		t.Fatalf("expected w-term-degraded to be degraded")
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

	state := model.State{Windows: []model.Window{{Key: "w-1", AppID: "firefox"}, {Key: "w-2", AppID: "Code"}}}
	planner := NewPlanner(PlannerConfig{AppAllowlist: map[string]string{" code ": "code"}, Terminal: TerminalConfig{Command: "kitty"}})
	plan := planner.Build(state)

	if statusOf(plan, "w-1") != StatusSkipped {
		t.Fatalf("expected firefox skipped")
	}
	if statusOf(plan, "w-2") != StatusReady {
		t.Fatalf("expected code ready")
	}
}

func TestTerminalRestoreMarksPartialMetadataAsDegraded(t *testing.T) {
	t.Parallel()

	state := model.State{Windows: []model.Window{
		{Key: "w-cwd-only", AppID: "kitty", Terminal: &model.Terminal{CWD: "/tmp/project"}},
		{Key: "w-session-only", AppID: "kitty", Terminal: &model.Terminal{SessionTag: "proj-a"}},
	}}
	planner := NewPlanner(PlannerConfig{Terminal: TerminalConfig{Command: "kitty", ZellijAttachOrCreate: true}})
	plan := planner.Build(state)

	if statusOf(plan, "w-cwd-only") != StatusDegraded {
		t.Fatalf("expected cwd-only terminal to be degraded")
	}
	if reasonOf(plan, "w-cwd-only") != "missing terminal session tag" {
		t.Fatalf("unexpected reason for cwd-only terminal: %q", reasonOf(plan, "w-cwd-only"))
	}
	if statusOf(plan, "w-session-only") != StatusDegraded {
		t.Fatalf("expected session-only terminal to be degraded")
	}
	if reasonOf(plan, "w-session-only") != "missing terminal cwd" {
		t.Fatalf("unexpected reason for session-only terminal: %q", reasonOf(plan, "w-session-only"))
	}
}

func TestExecutorContinueOnFailureSummaryAndResults(t *testing.T) {
	t.Parallel()

	plan := Plan{Items: []Item{
		{WindowKey: "w-1", Status: StatusReady, Command: "ok-1"},
		{WindowKey: "w-2", Status: StatusReady, Command: "fail"},
		{WindowKey: "w-3", Status: StatusSkipped},
		{WindowKey: "w-4", Status: StatusDegraded, Reason: "missing terminal session tag"},
		{WindowKey: "w-5", Status: StatusReady, Command: "ok-2"},
	}}

	executor := NewExecutor(stubRunner{failCommand: "fail"})
	result := executor.Execute(context.Background(), plan)

	if result.Summary.Restored != 2 || result.Summary.Skipped != 2 || result.Summary.Failed != 1 {
		t.Fatalf("unexpected summary: %+v", result.Summary)
	}
	if statusForResult(result.Items, "w-1") != StatusReady {
		t.Fatalf("expected w-1 to be ready result")
	}
	if statusForResult(result.Items, "w-2") != StatusFailed {
		t.Fatalf("expected w-2 to fail")
	}
	if errorForResult(result.Items, "w-2") != "boom" {
		t.Fatalf("expected w-2 error to be boom, got %q", errorForResult(result.Items, "w-2"))
	}
	if statusForResult(result.Items, "w-3") != StatusSkipped {
		t.Fatalf("expected w-3 skipped result")
	}
	if statusForResult(result.Items, "w-4") != StatusDegraded {
		t.Fatalf("expected w-4 degraded result")
	}
	if reasonForResult(result.Items, "w-4") != "missing terminal session tag" {
		t.Fatalf("expected w-4 degraded reason to propagate")
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

func reasonOf(plan Plan, key string) string {
	for _, item := range plan.Items {
		if item.WindowKey == key {
			return item.Reason
		}
	}
	return ""
}

func statusForResult(results []ItemResult, key string) Status {
	for _, result := range results {
		if result.WindowKey == key {
			return result.Status
		}
	}
	return ""
}

func reasonForResult(results []ItemResult, key string) string {
	for _, result := range results {
		if result.WindowKey == key {
			return result.Reason
		}
	}
	return ""
}

func errorForResult(results []ItemResult, key string) string {
	for _, result := range results {
		if result.WindowKey == key {
			return result.Error
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
