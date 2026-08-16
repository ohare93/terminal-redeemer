package mirror

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPinStoreRejectsMissingAndNullProjectionArrays(t *testing.T) {
	store, err := OpenPinStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.Write(testPin("seed"))
	if err != nil {
		t.Fatal(err)
	}
	for name, payload := range map[string]string{
		"missing": `{"v":1,"source_host":"lattice","source_profile":"default"}`,
		"null":    `{"v":1,"source_host":"lattice","source_profile":"default","projections":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Read("lattice", "default"); !errors.Is(err, ErrPinInvalid) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if err := os.WriteFile(path, []byte(`{"v":1,"source_host":"lattice","source_profile":"default","projections":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pin, err := store.Read("lattice", "default")
	if err != nil || pin.Projections == nil || len(pin.Projections) != 0 {
		t.Fatalf("pin=%#v error=%v", pin, err)
	}
}

func TestPinStoreRejectsSymlinkedStateAncestors(t *testing.T) {
	for _, stateAtLink := range []bool{false, true} {
		base := t.TempDir()
		outside := t.TempDir()
		link := filepath.Join(base, "link")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		state := link
		if !stateAtLink {
			state = filepath.Join(link, "state")
		}
		store, err := OpenPinStore(state)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Write(testPin("A")); err == nil {
			t.Fatalf("accepted symlinked state path %q", state)
		}
		entries, err := os.ReadDir(outside)
		if err != nil || len(entries) != 0 {
			t.Fatalf("escaped through state symlink: entries=%v err=%v", entries, err)
		}
	}
}

func TestPinStoreRejectsInnerDirectoryAndLockSymlinks(t *testing.T) {
	for _, relative := range []string{"mirror", "mirror/pins", "mirror/pin-locks"} {
		t.Run(relative, func(t *testing.T) {
			root := t.TempDir()
			outside := t.TempDir()
			path := filepath.Join(root, relative)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
			store, _ := OpenPinStore(root)
			if _, err := store.Write(testPin("A")); err == nil {
				t.Fatalf("accepted symlinked %s", relative)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("escaped through %s: entries=%v err=%v", relative, entries, err)
			}
		})
	}

	root := t.TempDir()
	store, _ := OpenPinStore(root)
	if _, err := store.Write(testPin("seed")); err != nil {
		t.Fatal(err)
	}
	lockName := strings.TrimSuffix(pinName("lattice", "default"), ".json") + ".lock"
	lockPath := filepath.Join(root, "mirror", "pin-locks", lockName)
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "lock-target")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, lockPath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(testPin("A")); err == nil {
		t.Fatal("accepted symlinked lock file")
	}
	if got, _ := os.ReadFile(target); string(got) != "unchanged" {
		t.Fatalf("lock symlink target changed: %q", got)
	}
}

func TestPinDescriptorPathSwapCannotRedirectPublication(t *testing.T) {
	root := t.TempDir()
	store, err := OpenPinStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(testPin("seed")); err != nil {
		t.Fatal(err)
	}
	pinsFD, err := store.openSecureChild("pins", false)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pinsFD)
	original := filepath.Join(root, "mirror", "pins")
	moved := filepath.Join(root, "mirror", "pins-old")
	outside := t.TempDir()
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, original); err != nil {
		t.Fatal(err)
	}
	name, file, err := createPinTemp(pinsFD)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("descriptor anchored")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := unix.Renameat(pinsFD, name, pinsFD, "anchored.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(moved, "anchored.json")); err != nil {
		t.Fatalf("anchored publication missing: %v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("path swap redirected publication: entries=%v err=%v", entries, err)
	}
}

func TestPinStoreRequiresSafeOwnedStateAndMirrorDirectories(t *testing.T) {
	t.Run("safe modes", func(t *testing.T) {
		for _, modes := range []struct {
			name   string
			state  os.FileMode
			mirror os.FileMode
		}{
			{name: "private", state: 0o700, mirror: 0o700},
			{name: "readable", state: 0o755, mirror: 0o755},
		} {
			t.Run(modes.name, func(t *testing.T) {
				stateDir := filepath.Join(t.TempDir(), "state")
				if err := os.Mkdir(stateDir, modes.state); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(stateDir, modes.state); err != nil {
					t.Fatal(err)
				}
				mirrorDir := filepath.Join(stateDir, "mirror")
				if err := os.Mkdir(mirrorDir, modes.mirror); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(mirrorDir, modes.mirror); err != nil {
					t.Fatal(err)
				}
				store, err := OpenPinStore(stateDir)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.Write(testPin("A")); err != nil {
					t.Fatalf("safe state=%04o mirror=%04o rejected: %v", modes.state, modes.mirror, err)
				}
			})
		}
	})

	for _, location := range []string{"state", "mirror"} {
		t.Run("unsafe "+location+" modes", func(t *testing.T) {
			for _, mode := range []os.FileMode{0o777, 0o775, 0o702} {
				t.Run(fmt.Sprintf("%04o", mode), func(t *testing.T) {
					stateDir := filepath.Join(t.TempDir(), "state")
					if err := os.Mkdir(stateDir, 0o700); err != nil {
						t.Fatal(err)
					}
					mirrorDir := filepath.Join(stateDir, "mirror")
					if err := os.Mkdir(mirrorDir, 0o700); err != nil {
						t.Fatal(err)
					}
					target := stateDir
					if location == "mirror" {
						target = mirrorDir
					}
					if err := os.Chmod(target, mode); err != nil {
						t.Fatal(err)
					}
					store, err := OpenPinStore(stateDir)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := store.Write(testPin("A")); err == nil {
						t.Fatalf("accepted unsafe %s mode %04o", location, mode)
					}
				})
			}
		})
	}

	otherUID := uint32(0)
	if os.Geteuid() == 0 {
		otherUID = 1
	}
	wrongOwner := unix.Stat_t{Mode: unix.S_IFDIR | 0o700, Uid: otherUID}
	if err := validateOwnedSafeDirectory(wrongOwner, "test directory"); err == nil {
		t.Fatal("accepted directory owned by another user")
	}
}

func TestPinStoreWorksAfterAttachLocalCreatesMirrorDirectory(t *testing.T) {
	stateDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lock, err := acquireAttachLock(ctx, stateDir, generatedTestSession, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	mirrorInfo, err := os.Stat(filepath.Join(stateDir, "mirror"))
	if err != nil {
		t.Fatal(err)
	}
	if mirrorInfo.Mode().Perm() != 0o755 {
		t.Fatalf("attach-local mirror mode=%04o", mirrorInfo.Mode().Perm())
	}
	store, err := OpenPinStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	want := testPin("A")
	if _, err := store.Write(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Read("lattice", "default")
	if err != nil || got.Projections[0].Session != "A" {
		t.Fatalf("pin=%#v error=%v", got, err)
	}
}
