package procmeta

type NoopReader struct{}

func (NoopReader) Inspect(_ int) (ProcessInfo, error) {
	return ProcessInfo{}, nil
}
