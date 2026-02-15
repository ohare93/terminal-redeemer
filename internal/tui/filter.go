package tui

import "github.com/jmo/terminal-redeemer/internal/restore"

func FilterPlan(plan restore.Plan, selected map[string]bool) restore.Plan {
	filtered := restore.Plan{Items: make([]restore.Item, 0, len(plan.Items))}
	for _, item := range plan.Items {
		out := item
		if out.Status == restore.StatusReady && !selected[out.WindowKey] {
			out.Status = restore.StatusSkipped
			out.Reason = "excluded in tui"
			out.Command = ""
		}
		filtered.Items = append(filtered.Items, out)
	}
	return filtered
}
