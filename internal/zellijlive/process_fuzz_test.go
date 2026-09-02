package zellijlive

import (
	"strings"
	"testing"

	"github.com/jmo/terminal-redeemer/internal/procmeta"
)

func FuzzExactZellijAttachSession(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte("zellij\x00attach\x00--\x00safe-session\x00"),
		[]byte("zellij\x00attach\x00../unsafe\x00"),
		append([]byte("zellij\x00attach\x00--\x00"), 0xff, 0),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, payload []byte) {
		parts := strings.Split(strings.TrimSuffix(string(payload), "\x00"), "\x00")
		session, ok := procmeta.ExactZellijAttachSession(parts)
		if ok && (len(parts) != 4 || session != parts[3]) {
			t.Fatalf("inconsistent exact parse: %#v %q", parts, session)
		}
	})
}
