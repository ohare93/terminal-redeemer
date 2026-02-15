package doctor

import "context"

type Status string

const (
	StatusPass Status = "pass"
	StatusFail Status = "fail"
)

type Result struct {
	Name   string
	Status Status
	Detail string
}

type Check interface {
	Name() string
	Run(context.Context) Result
}

type Summary struct {
	Total  int
	Passed int
	Failed int
}

func Run(ctx context.Context, checks []Check) []Result {
	results := make([]Result, 0, len(checks))
	for _, check := range checks {
		result := check.Run(ctx)
		if result.Name == "" {
			result.Name = check.Name()
		}
		if result.Status == "" {
			result.Status = StatusFail
		}
		results = append(results, result)
	}
	return results
}

func Summarize(results []Result) Summary {
	summary := Summary{Total: len(results)}
	for _, result := range results {
		if result.Status == StatusPass {
			summary.Passed++
			continue
		}
		summary.Failed++
	}
	return summary
}

func HasFailures(results []Result) bool {
	for _, result := range results {
		if result.Status != StatusPass {
			return true
		}
	}
	return false
}
