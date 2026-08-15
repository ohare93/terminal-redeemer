package mirror

import (
	"fmt"
	"regexp"
)

var generatedSessionPattern = regexp.MustCompile(`^redeem-[a-f0-9]{32}$`)

// NewSessionName returns a collision-resistant Zellij-safe identity reserved
// for explicit mirror-new launches.
func NewSessionName() (string, error) {
	id, err := RandomID()
	if err != nil {
		return "", fmt.Errorf("generate mirror session name: %w", err)
	}
	return "redeem-" + id, nil
}
