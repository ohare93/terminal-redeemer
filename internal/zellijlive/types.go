package zellijlive

import (
	"context"

	"github.com/jmo/terminal-redeemer/internal/procmeta"
)

type Status string

const (
	StatusActive            Status = "active"
	StatusDeadResurrectable Status = "dead_resurrectable"
	StatusMissing           Status = "missing"
	StatusPrefixOnly        Status = "prefix_only"
	StatusDuplicate         Status = "duplicate"
	StatusSocketInvalid     Status = "socket_invalid"
)

type Session struct {
	Name   string
	ID     string
	Status Status
}

type Catalog struct {
	Sessions                   map[string]Session
	Names                      []string
	ResurrectionCacheAvailable bool
}

func (catalog Catalog) Exact(name string) Session {
	if session, ok := catalog.Sessions[name]; ok {
		return session
	}
	prefix := false
	for _, candidate := range catalog.Names {
		if len(candidate) > len(name) && candidate[:len(name)] == name {
			prefix = true
			break
		}
	}
	if prefix {
		return Session{Name: name, Status: StatusPrefixOnly}
	}
	return Session{Name: name, Status: StatusMissing}
}

type Cataloger interface {
	Observe(context.Context) (Catalog, error)
}

type ProcessEvidence = procmeta.ZellijSessionEvidence

type ProcessObserver interface {
	Observe(context.Context, int) (ProcessEvidence, error)
}
