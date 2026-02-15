package restore

import "context"

type CommandRunner interface {
	Run(ctx context.Context, command string) error
}

type Executor struct {
	runner CommandRunner
}

func NewExecutor(runner CommandRunner) *Executor {
	return &Executor{runner: runner}
}

type Summary struct {
	Restored int
	Skipped  int
	Failed   int
}

type ItemResult struct {
	WindowKey string
	Status    Status
	Reason    string
	Error     string
}

type Result struct {
	Summary Summary
	Items   []ItemResult
}

func (e *Executor) Execute(ctx context.Context, plan Plan) Result {
	summary := Summary{}
	results := make([]ItemResult, 0, len(plan.Items))
	for _, item := range plan.Items {
		if item.Status != StatusReady {
			summary.Skipped++
			results = append(results, ItemResult{WindowKey: item.WindowKey, Status: item.Status, Reason: item.Reason})
			continue
		}
		if err := e.runner.Run(ctx, item.Command); err != nil {
			summary.Failed++
			results = append(results, ItemResult{WindowKey: item.WindowKey, Status: StatusFailed, Error: err.Error()})
			continue
		}
		summary.Restored++
		results = append(results, ItemResult{WindowKey: item.WindowKey, Status: StatusReady})
	}
	return Result{Summary: summary, Items: results}
}
