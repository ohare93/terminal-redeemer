package niri

import (
	"context"
	"os"
)

type FileSnapshotter struct {
	Path string
}

func (f FileSnapshotter) Snapshot(_ context.Context) ([]byte, error) {
	return os.ReadFile(f.Path)
}
