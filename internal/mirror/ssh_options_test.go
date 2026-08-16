package mirror

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestValidateSSHOptionsRejectsOperandsAndDeceptiveVectors(t *testing.T) {
	valid := [][]string{
		nil,
		{"-p", "2222", "-i", "/tmp/key", "-o", "BatchMode=yes", "-Fconfig", "-J", "jump", "-vv", "-T"},
		{"-p2222", "-i/tmp/key", "-oBatchMode=yes"},
	}
	for _, options := range valid {
		if err := ValidateSSHOptions(options); err != nil {
			t.Fatalf("valid options %#v: %v", options, err)
		}
	}
	invalid := [][]string{
		{"evil.example"},
		{"--"},
		{"-p"},
		{"-p", "70000"},
		{"-i", "/tmp/key", "extra.example"},
		{"--hostname=evil"},
		{"-Z"},
		{"-o", "BatchMode=yes", "--", "evil.example"},
	}
	for _, options := range invalid {
		if err := ValidateSSHOptions(options); err == nil {
			t.Fatalf("accepted options %#v", options)
		}
	}
}

func TestSharedSSHBuilderEmitsOneAuthoritativeTail(t *testing.T) {
	args, err := buildSSHArgs([]string{"-p", "2222", "-oBatchMode=yes"}, []string{"-tt"}, "user@lattice", "'redeem' 'mirror' 'snapshot'")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-p", "2222", "-oBatchMode=yes", "-tt", "--", "user@lattice", "'redeem' 'mirror' 'snapshot'"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args=%#v want=%#v", args, want)
	}
}

func TestSSHOptionValidationPrecedesRunnerSideEffects(t *testing.T) {
	runner := &recordingRunner{outputs: []outputResult{{data: validRemoteSnapshot()}}}
	_, err := AcquireRemote(context.Background(), runner, RemoteConfig{
		Host: "lattice", SSHCommand: "ssh", SSHOptions: []string{"evil.example"}, SnapshotCommand: []string{"redeem", "mirror", "snapshot"},
	})
	if err == nil || len(runner.outputCalls) != 0 || len(runner.runCalls) != 0 {
		t.Fatalf("err=%v output=%#v run=%#v", err, runner.outputCalls, runner.runCalls)
	}
	if _, err := PlanLaunch(Window{ZellijSession: "A"}, LaunchConfig{SourceHost: "lattice", SSHCommand: "ssh", SSHOptions: []string{"--"}, LauncherCommand: "kitty", AppID: "owned"}); err == nil {
		t.Fatal("PlanLaunch accepted configured option boundary")
	}
	if _, err := PlanSourceAttach(SourceAttachConfig{SourceHost: "lattice", SSHCommand: "ssh", SSHOptions: []string{"-p"}, SnapshotCommand: []string{"redeem", "mirror", "snapshot"}, Session: generatedTestSession}); err == nil {
		t.Fatal("PlanSourceAttach accepted missing option argument")
	}

	stateDir := filepath.Join(t.TempDir(), "absent")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = ApplyPinned(ctx, ApplyConfig{
		Snapshot: activeSnapshot("lattice", "A"), SourceHost: "lattice", SSHCommand: "ssh", SSHOptions: []string{"evil.example"},
		LauncherCommand: "kitty", AppID: "owned", NiriCommand: "niri", StateDir: stateDir, Timeout: time.Second, PollInterval: time.Millisecond,
	}, ApplyDeps{Runner: runner})
	if err == nil {
		t.Fatal("ApplyPinned accepted operand in SSH options")
	}
	if _, statErr := os.Stat(stateDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid apply options mutated state: %v", statErr)
	}
}
