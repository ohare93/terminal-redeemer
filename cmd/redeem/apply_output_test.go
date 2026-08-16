package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jmo/terminal-redeemer/internal/mirror"
	"github.com/jmo/terminal-redeemer/internal/resume"
)

func TestWriteMirrorApplyResultReportsPlacementDegradation(t *testing.T) {
	var output bytes.Buffer
	failed := writeMirrorApplyResult(&output, mirror.ApplyResult{Items: []mirror.ApplyItem{{
		PinnedProjection: mirror.PinnedProjection{Session: "A", Order: 3},
		Status:           mirror.ApplyOpened,
		WindowID:         42,
		LayoutStatus:     resume.LayoutDegraded,
		LayoutReason:     "workspace move failed; size unavailable",
	}}})
	if failed {
		t.Fatal("placement-only degradation changed attachment success exit policy")
	}
	text := output.String()
	for _, want := range []string{"status=opened", "window_id=42", "layout_status=degraded", `layout_reason="workspace move failed; size unavailable"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("output %q lacks %q", text, want)
		}
	}
}
