package projectidentity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPaletteMatchesMonoProjectFooter(t *testing.T) {
	for _, tc := range []struct {
		path string
		want RGB
	}{
		{"/home/jmo/Development/projects/waterfill", RGB{187, 17, 119}},
		{"/home/jmo/Development/mono/agent", RGB{192, 175, 22}},
		{"/home/jmo/Development/workspaces/agent/agentleman-real", RGB{177, 73, 32}},
	} {
		background, foreground := Palette(tc.path)
		if background != tc.want || foreground != (RGB{255, 255, 255}) {
			t.Fatalf("Palette(%q)=(%v,%v), want (%v,white)", tc.path, background, foreground, tc.want)
		}
	}
}

func TestPaletteUsesJavaScriptUTF16Units(t *testing.T) {
	// Fixture calculated with Mono project-footer's JS fnv1a/keyedPalette.
	background, foreground := Palette("/home/jmo/Development/projects/🚀-café")
	if background != (RGB{20, 74, 173}) || foreground != (RGB{255, 255, 255}) {
		t.Fatalf("non-ASCII palette=(%v,%v), want RGB(20,74,173),white", background, foreground)
	}
}

func TestResolveDirectAndCanonicalWorkspace(t *testing.T) {
	root := t.TempDir()
	development := filepath.Join(root, "Development")
	repository := filepath.Join(development, "mono", "agent")
	workspace := filepath.Join(development, "workspaces", "agent", "agentleman-real")
	store := filepath.Join(repository, ".jj", "repo")
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, ".jj"), 0o700); err != nil {
		t.Fatal(err)
	}
	pointer, err := filepath.Rel(filepath.Join(workspace, ".jj"), store)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".jj", "repo"), []byte(pointer+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	segments := Resolve(filepath.Join(workspace, "subdir"))
	if len(segments) != 2 || segments[0].Label != "mono/agent" || segments[1].Label != "agentleman-real" {
		t.Fatalf("workspace identity=%+v", segments)
	}
	direct := Resolve(filepath.Join(development, "projects", "waterfill"))
	if len(direct) != 1 || direct[0].Label != "projects/waterfill" {
		t.Fatalf("direct identity=%+v", direct)
	}
}

func TestResolveRejectsUnsafePointersAndSanitizesLabels(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "Development", "workspaces", "bad\x1bproject", "line\nbreak")
	if err := os.MkdirAll(filepath.Join(workspace, ".jj"), 0o700); err != nil {
		t.Fatal(err)
	}
	pointerPath := filepath.Join(workspace, ".jj", "repo")
	for _, payload := range [][]byte{
		[]byte("one\ntwo"),
		append([]byte("bad"), 0, 'x'),
		[]byte(strings.Repeat("x", maxRepoPointerBytes+1)),
	} {
		if err := os.WriteFile(pointerPath, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		segments := Resolve(workspace)
		if len(segments) != 2 || strings.ContainsAny(segments[0].Label+segments[1].Label, "\x1b\n\r") {
			t.Fatalf("unsafe fallback identity=%+v", segments)
		}
	}
}
