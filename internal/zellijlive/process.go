package zellijlive

import (
	"context"
	"errors"
	"fmt"

	"github.com/jmo/terminal-redeemer/internal/procmeta"
)

var ErrProcessObservationIncomplete = errors.New("process observation incomplete")

type ProcObserver struct{ ProcRoot string }

// Observe is the zellijlive-facing adapter for the shared procmeta evidence.
// Process walking and exact attach parsing intentionally remain in procmeta so
// mirror, capture, and resume all rely on one implementation.
func (observer ProcObserver) Observe(ctx context.Context, pid int) (ProcessEvidence, error) {
	if pid <= 0 {
		return ProcessEvidence{}, fmt.Errorf("invalid Kitty PID")
	}
	evidence, err := procmeta.ObserveZellijSessionEvidence(ctx, observer.ProcRoot, pid)
	if err != nil {
		return ProcessEvidence{}, err
	}
	if !evidence.Complete {
		return ProcessEvidence{}, fmt.Errorf("%w: pid %d", ErrProcessObservationIncomplete, pid)
	}
	return evidence, nil
}
