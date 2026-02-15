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

func (e *Executor) Execute(ctx context.Context, plan Plan) Summary {
	summary := Summary{}
	for _, item := range plan.Items {
		if item.Status != StatusReady {
			summary.Skipped++
			continue
		}
		if err := e.runner.Run(ctx, item.Command); err != nil {
			summary.Failed++
			continue
		}
		summary.Restored++
	}
	return summary
}
