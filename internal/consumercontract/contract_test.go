package consumercontract_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/jmo/terminal-redeemer/internal/config"
	"github.com/jmo/terminal-redeemer/internal/slicecontroller"
	"github.com/jmo/terminal-redeemer/internal/sliceprotocol"
	"github.com/jmo/terminal-redeemer/internal/slicerpc"
	"github.com/jmo/terminal-redeemer/internal/zellijlive"
)

type runtimeContract struct {
	ContractVersion string `json:"contract_version"`
	Protocol        struct {
		InventorySchemaVersions  []int  `json:"inventory_schema_versions"`
		RPCSchemaVersions        []int  `json:"rpc_schema_versions"`
		ControllerSchemaVersions []int  `json:"controller_schema_versions"`
		WorkspaceNormalization   string `json:"workspace_normalization"`
	} `json:"protocol"`
	Compatibility struct {
		NiriVersion   string `json:"niri_version"`
		ZellijVersion string `json:"zellij_version"`
	} `json:"compatibility"`
	Selection struct {
		Formula                        string `json:"formula"`
		AllEligibleScope               string `json:"all_eligible_scope"`
		UnnamedSpatialPolicy           string `json:"unnamed_spatial_policy"`
		RoutedLaunchPolicy             string `json:"routed_launch_policy"`
		AllEligibleStateField          string `json:"all_eligible_state_field"`
		AllEligibleUndoable            bool   `json:"all_eligible_undoable"`
		PriorReaderBehaviorWhenEnabled string `json:"prior_reader_behavior_when_enabled"`
		DowngradeRequiresDisableFirst  bool   `json:"downgrade_requires_disable_first"`
	} `json:"selection"`
	Defaults struct {
		LeechModeEnabled        bool   `json:"leech_mode_enabled"`
		ControllerEnabled       bool   `json:"controller_enabled"`
		SliceClipboardEnabled   bool   `json:"slice_clipboard_enabled"`
		AuthorityMode           string `json:"authority_mode"`
		LeechWriteAuthorized    bool   `json:"leech_write_authorized"`
		PollInterval            string `json:"poll_interval"`
		ControlTimeout          string `json:"control_timeout"`
		RetryWindow             string `json:"retry_window"`
		SourceGoneGrace         string `json:"source_gone_grace"`
		SourceGoneConfirmations int    `json:"source_gone_confirmations"`
	} `json:"defaults"`
	Commands struct {
		Manage               []string `json:"manage"`
		ControllerOperations []string `json:"controller_operations"`
	} `json:"commands"`
	Configuration struct {
		ReadOnlyOptions []string `json:"read_only_options"`
	} `json:"configuration"`
	Integration struct {
		ManageHelperOption string `json:"manage_helper_option"`
	} `json:"integration"`
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(workingDir, "..", ".."))
}

func contractDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(repositoryRoot(t), "contracts", "host-leech-slices", "v1")
}

func readStrictJSON(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(contractDir(t), name))
	if err != nil {
		t.Fatal(err)
	}
	if err := sliceprotocol.RejectDuplicateKeys(raw); err != nil {
		t.Fatalf("strict JSON validation of %s: %v", name, err)
	}
	return raw
}

func TestConsumerContractRuntimeValues(t *testing.T) {
	raw := readStrictJSON(t, "consumer-contract.json")
	var contract runtimeContract
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}

	if contract.ContractVersion != "1.2.0" {
		t.Fatalf("contract version=%q, want 1.2.0", contract.ContractVersion)
	}
	if got, want := contract.Protocol.InventorySchemaVersions, []int{int(sliceprotocol.SchemaVersion)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("inventory schema versions=%v, want runtime %v", got, want)
	}
	if got, want := contract.Protocol.RPCSchemaVersions, []int{int(slicerpc.SchemaVersion)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("RPC schema versions=%v, want runtime %v", got, want)
	}
	if got, want := contract.Protocol.ControllerSchemaVersions, []int{slicecontroller.SchemaVersion}; !reflect.DeepEqual(got, want) {
		t.Fatalf("controller schema versions=%v, want runtime %v", got, want)
	}
	if contract.Protocol.WorkspaceNormalization != sliceprotocol.WorkspaceNormalization {
		t.Fatalf("workspace normalization=%q, want runtime %q", contract.Protocol.WorkspaceNormalization, sliceprotocol.WorkspaceNormalization)
	}
	selection := contract.Selection
	if selection.Formula != "(all_eligible OR selected_workspace OR exact_pickup) AND NOT closed_by_user" ||
		selection.AllEligibleScope != "current_and_future_eligible_sources_including_unnamed_workspaces" ||
		selection.UnnamedSpatialPolicy != "attach_without_cross_machine_spatial_placement" ||
		selection.RoutedLaunchPolicy != "explicit_selected_named_workspaces_only" ||
		selection.AllEligibleStateField != "optional_omitempty_boolean" || selection.AllEligibleUndoable ||
		selection.PriorReaderBehaviorWhenEnabled != "reject_unknown_field" || !selection.DowngradeRequiresDisableFirst {
		t.Fatalf("selection contract drifted: %#v", selection)
	}
	baseState := slicecontroller.NewState(slicecontroller.Namespace{Host: "host", Leech: "leech"}, "controller-test")
	baseJSON, err := json.Marshal(baseState)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(baseJSON, []byte(`"all_eligible"`)) {
		t.Fatalf("false all_eligible must be omitted: %s", baseJSON)
	}
	baseState.AllEligible = true
	enabledJSON, err := json.Marshal(baseState)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(enabledJSON, []byte(`"all_eligible":true`)) {
		t.Fatalf("enabled all_eligible must be explicit: %s", enabledJSON)
	}

	cfg := config.Defaults()
	got := contract.Defaults
	if got.LeechModeEnabled != cfg.Slice.LeechModeEnabled ||
		got.ControllerEnabled != cfg.Slice.Controller.Enabled ||
		got.SliceClipboardEnabled != cfg.Slice.Clipboard.Enabled ||
		got.AuthorityMode != cfg.Slice.Controller.AuthorityMode ||
		got.LeechWriteAuthorized != cfg.Slice.Controller.LeechWriteAuthorized ||
		got.PollInterval != cfg.Slice.Controller.PollInterval.String() ||
		got.ControlTimeout != cfg.Slice.Controller.ControlTimeout.String() ||
		got.RetryWindow != cfg.Slice.Controller.RetryWindow.String() ||
		got.SourceGoneGrace != cfg.Slice.Controller.SourceGoneGrace.String() ||
		got.SourceGoneConfirmations != cfg.Slice.Controller.SourceGoneConfirmations {
		t.Fatalf("contract defaults drifted from runtime: contract=%#v runtime=%#v", got, cfg.Slice)
	}
	if contract.Compatibility.NiriVersion != cfg.Slice.ExpectedNiriVersion || contract.Compatibility.ZellijVersion != zellijlive.PinnedVersion {
		t.Fatalf("pinned compatibility drifted: contract=%#v runtime niri=%q zellij=%q", contract.Compatibility, cfg.Slice.ExpectedNiriVersion, zellijlive.PinnedVersion)
	}

	if got, want := contract.Commands.Manage, []string{"$REDEEM", "slice", "manage"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("manage command=%v, want %v", got, want)
	}
	wantOperations := []string{"workspace-add", "workspace-remove", "all-enable", "all-disable", "pickup", "pickup-remove", "drop", "close", "reopen", "undo", "reconnect", "launch-handoff"}
	if !reflect.DeepEqual(contract.Commands.ControllerOperations, wantOperations) {
		t.Fatalf("controller operations=%v, want %v", contract.Commands.ControllerOperations, wantOperations)
	}
	if !contains(contract.Configuration.ReadOnlyOptions, "manageCommand") || contract.Integration.ManageHelperOption != "programs.terminal-redeemer.slice.manageCommand" {
		t.Fatalf("manage module contract drifted: read-only=%v helper=%q", contract.Configuration.ReadOnlyOptions, contract.Integration.ManageHelperOption)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestConsumerContractStrictJSON(t *testing.T) {
	tests := []struct {
		name         string
		duplicateKey string
	}{
		{name: "consumer-contract.json", duplicateKey: `"schema_version":0,`},
		{name: "consumer-contract.schema.json", duplicateKey: `"$schema":"duplicate",`},
	}
	for _, tc := range tests {
		raw := readStrictJSON(t, tc.name)
		if len(raw) == 0 || raw[0] != '{' {
			t.Fatalf("%s is not a JSON object", tc.name)
		}
		mutated := append([]byte("{"+tc.duplicateKey), raw[1:]...)
		if err := sliceprotocol.RejectDuplicateKeys(mutated); err == nil {
			t.Fatalf("production strict JSON gate accepted duplicate key in %s", tc.name)
		}
	}
}

func validateRepositoryRelativeMarkdownLinks(root string) error {
	documents := []string{filepath.Join(root, "README.md")}
	if err := filepath.WalkDir(filepath.Join(root, "docs"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(path), ".md") {
			documents = append(documents, path)
		}
		return nil
	}); err != nil {
		return err
	}

	markdownLink := regexp.MustCompile(`\[[^]]*\]\(([^)]+)\)`)
	for _, document := range documents {
		raw, err := os.ReadFile(document)
		if err != nil {
			return err
		}
		for _, match := range markdownLink.FindAllStringSubmatch(string(raw), -1) {
			target := strings.TrimSpace(match[1])
			if strings.HasPrefix(target, "<") && strings.HasSuffix(target, ">") {
				target = strings.TrimSuffix(strings.TrimPrefix(target, "<"), ">")
			}
			target = strings.SplitN(target, " ", 2)[0]
			target = strings.SplitN(target, "#", 2)[0]
			if target == "" || filepath.IsAbs(target) || strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(document), filepath.FromSlash(target))); err != nil {
				relativeDocument, relErr := filepath.Rel(root, document)
				if relErr != nil {
					relativeDocument = document
				}
				return fmt.Errorf("%s: relative Markdown link %q: %w", relativeDocument, target, err)
			}
		}
	}
	return nil
}

func TestRepositoryRelativeMarkdownLinks(t *testing.T) {
	if err := validateRepositoryRelativeMarkdownLinks(repositoryRoot(t)); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryRelativeMarkdownLinksRejectMissingTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("[missing](docs/not-present.md)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateRepositoryRelativeMarkdownLinks(root); err == nil {
		t.Fatal("missing repository-relative Markdown link was accepted")
	}
}

func TestConsumerContractSourcePackageMembers(t *testing.T) {
	base := contractDir(t)
	for _, name := range []string{
		"consumer-contract.json",
		"consumer-contract.schema.json",
		"niri-bindings.kdl.in",
	} {
		info, err := os.Stat(filepath.Join(base, name))
		if err != nil {
			t.Errorf("required contract member %s: %v", name, err)
		} else if !info.Mode().IsRegular() {
			t.Errorf("required contract member %s is not a regular file", name)
		}
	}
}
