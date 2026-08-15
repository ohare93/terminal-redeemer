package sliceprotocol

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
)

var opaqueIdentityPattern = regexp.MustCompile(`^(src_|ses_)[A-Za-z0-9_-]{43}$`)
var safeSessionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

var ErrInvalid = errors.New("invalid slice protocol payload")

func ValidateEnvelope(envelope Envelope) error {
	if envelope.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: unsupported schema version %d", ErrInvalid, envelope.SchemaVersion)
	}
	if err := boundedRequired("source_host_id", envelope.SourceHostID); err != nil {
		return err
	}
	if !ValidUUID(envelope.SourceHostID) {
		return fmt.Errorf("%w: source_host_id must be a UUIDv4", ErrInvalid)
	}
	if envelope.Observation.AttemptedAt.IsZero() {
		return fmt.Errorf("%w: attempted_at is required", ErrInvalid)
	}
	switch envelope.Observation.Quality {
	case QualityComplete:
		if envelope.Authoritative == nil {
			return fmt.Errorf("%w: complete observation requires authority", ErrInvalid)
		}
		if len(envelope.Observation.DegradedReasons) != 0 {
			return fmt.Errorf("%w: complete observation cannot have degraded reasons", ErrInvalid)
		}
	case QualityDegraded:
		if len(envelope.Observation.DegradedReasons) == 0 {
			return fmt.Errorf("%w: degraded observation requires a reason", ErrInvalid)
		}
		for _, reason := range envelope.Observation.DegradedReasons {
			if !validReason(reason.Code) {
				return fmt.Errorf("%w: unknown degraded reason %q", ErrInvalid, reason.Code)
			}
		}
	default:
		return fmt.Errorf("%w: unknown quality %q", ErrInvalid, envelope.Observation.Quality)
	}
	if envelope.Authoritative != nil {
		if err := ValidateAuthoritative(*envelope.Authoritative); err != nil {
			return err
		}
	}
	return nil
}

func ValidateAuthoritative(authority Authoritative) error {
	if err := boundedRequired("source_epoch", authority.SourceEpoch); err != nil {
		return err
	}
	if !ValidUUID(authority.SourceEpoch) {
		return fmt.Errorf("%w: source_epoch must be a UUIDv4", ErrInvalid)
	}
	if authority.Revision == 0 || authority.ObservedAt.IsZero() {
		return fmt.Errorf("%w: positive revision and observed_at are required", ErrInvalid)
	}
	if authority.WorkspaceNormalization != WorkspaceNormalization {
		return fmt.Errorf("%w: unsupported workspace normalization %q", ErrInvalid, authority.WorkspaceNormalization)
	}
	if authority.LiveSessionIDs == nil || authority.Sources == nil || authority.Conflicts == nil {
		return fmt.Errorf("%w: live_session_ids, sources, and conflicts arrays are required", ErrInvalid)
	}
	liveSessionIDs := make(map[string]struct{}, len(authority.LiveSessionIDs))
	for _, id := range authority.LiveSessionIDs {
		if !ValidSessionID(id) {
			return fmt.Errorf("%w: live session identity is invalid", ErrInvalid)
		}
		if _, found := liveSessionIDs[id]; found {
			return fmt.Errorf("%w: duplicate live session identity", ErrInvalid)
		}
		liveSessionIDs[id] = struct{}{}
	}
	sourceIDs := make(map[string]struct{}, len(authority.Sources))
	sessionIDs := make(map[string]struct{}, len(authority.Sources))
	for i, source := range authority.Sources {
		if err := validateSource(source); err != nil {
			return err
		}
		if i > 0 && (source.Output != nil) != (authority.Sources[0].Output != nil) {
			return fmt.Errorf("%w: sources must consistently include or omit output geometry", ErrInvalid)
		}
		if _, found := sourceIDs[source.SourceID]; found {
			return fmt.Errorf("%w: duplicate source_id", ErrInvalid)
		}
		if _, found := sessionIDs[source.Session.ID]; found {
			return fmt.Errorf("%w: duplicate eligible session binding", ErrInvalid)
		}
		if _, live := liveSessionIDs[source.Session.ID]; !live {
			return fmt.Errorf("%w: eligible source session is absent from live_session_ids", ErrInvalid)
		}
		sourceIDs[source.SourceID] = struct{}{}
		sessionIDs[source.Session.ID] = struct{}{}
	}
	for _, conflict := range authority.Conflicts {
		if !validConflict(conflict.Code) {
			return fmt.Errorf("%w: unknown conflict code %q", ErrInvalid, conflict.Code)
		}
		if len(conflict.SourceID) > MaxStringBytes || len(conflict.SessionID) > MaxStringBytes {
			return fmt.Errorf("%w: conflict identity too long", ErrInvalid)
		}
		if conflict.SourceID != "" && (!opaqueIdentityPattern.MatchString(conflict.SourceID) || !strings.HasPrefix(conflict.SourceID, "src_")) {
			return fmt.Errorf("%w: conflict source identity is invalid", ErrInvalid)
		}
		if conflict.SessionID != "" && (!opaqueIdentityPattern.MatchString(conflict.SessionID) || !strings.HasPrefix(conflict.SessionID, "ses_")) {
			return fmt.Errorf("%w: conflict session identity is invalid", ErrInvalid)
		}
	}
	return nil
}

func validateSource(source Source) error {
	if err := boundedRequired("source_id", source.SourceID); err != nil {
		return err
	}
	if !opaqueIdentityPattern.MatchString(source.SourceID) || !strings.HasPrefix(source.SourceID, "src_") {
		return fmt.Errorf("%w: source_id is not an opaque v1 identity", ErrInvalid)
	}
	if source.RuntimeWindowID == 0 || source.Workspace.RuntimeID == 0 {
		return fmt.Errorf("%w: runtime window/workspace IDs must be positive", ErrInvalid)
	}
	if err := boundedRequired("session.id", source.Session.ID); err != nil {
		return err
	}
	if !ValidSessionID(source.Session.ID) {
		return fmt.Errorf("%w: session.id is not an opaque v1 identity", ErrInvalid)
	}
	if err := boundedRequired("session.name", source.Session.Name); err != nil {
		return err
	}
	if !ValidSessionName(source.Session.Name) {
		return fmt.Errorf("%w: session.name is unsafe", ErrInvalid)
	}
	if source.Session.Status != "active" {
		return fmt.Errorf("%w: eligible session must be active", ErrInvalid)
	}
	if len(source.Workspace.Name) > MaxStringBytes || len(source.Workspace.Key) > MaxWorkspaceKeyBytes {
		return fmt.Errorf("%w: workspace metadata is too long", ErrInvalid)
	}
	if source.Workspace.Name == "" && source.Workspace.Key != "" {
		return fmt.Errorf("%w: unnamed workspace cannot have a key", ErrInvalid)
	}
	if source.Workspace.Name != "" {
		key, err := NormalizeWorkspaceName(source.Workspace.Name)
		if err != nil || key != source.Workspace.Key {
			return fmt.Errorf("%w: workspace key mismatch", ErrInvalid)
		}
	}
	if source.Output != nil {
		if err := boundedRequired("output.name", source.Output.Name); err != nil {
			return err
		}
		if len(source.Output.Transform) > MaxStringBytes || strings.ContainsRune(source.Output.Transform, 0) {
			return fmt.Errorf("%w: output transform is invalid", ErrInvalid)
		}
		if source.Output.LogicalWidth <= 0 || source.Output.LogicalHeight <= 0 || !finitePositive(source.Output.Scale) {
			return fmt.Errorf("%w: invalid output geometry", ErrInvalid)
		}
	}
	if !finiteNonnegative(source.Layout.TileWidth) || !finiteNonnegative(source.Layout.TileHeight) || source.Layout.WindowWidth <= 0 || source.Layout.WindowHeight <= 0 {
		return fmt.Errorf("%w: invalid layout geometry", ErrInvalid)
	}
	switch source.Layout.Mode {
	case "tiled":
		if source.Layout.Position == nil || source.Layout.Position.Column <= 0 || source.Layout.Position.Tile <= 0 {
			return fmt.Errorf("%w: tiled layout needs a positive position", ErrInvalid)
		}
	case "floating":
		if source.Layout.Position != nil {
			return fmt.Errorf("%w: floating layout cannot have tiled position", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unknown layout mode %q", ErrInvalid, source.Layout.Mode)
	}
	return nil
}

func boundedRequired(name, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalid, name)
	}
	if len(value) > MaxStringBytes || strings.ContainsRune(value, 0) {
		return fmt.Errorf("%w: %s is invalid", ErrInvalid, name)
	}
	return nil
}

func ValidSessionID(value string) bool {
	return opaqueIdentityPattern.MatchString(value) && strings.HasPrefix(value, "ses_")
}

func ValidSessionName(value string) bool {
	return safeSessionPattern.MatchString(value) && len(value) <= 255
}

func ValidUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || value[14] != '4' {
		return false
	}
	variant := value[19]
	if variant != '8' && variant != '9' && variant != 'a' && variant != 'b' && variant != 'A' && variant != 'B' {
		return false
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil && len(decoded) == 16
}

func finitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
func finiteNonnegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validReason(code ReasonCode) bool {
	switch code {
	case ReasonNiriSocketUnavailable, ReasonNiriReplayTimeout, ReasonNiriReplayEOF, ReasonNiriConfigFailed,
		ReasonNiriMalformed, ReasonNiriReplyTooLarge, ReasonNiriMissingWorkspace, ReasonNiriMissingOutput,
		ReasonNiriInvalidGeometry, ReasonNiriUnsupportedTopology, ReasonSourceIdentityChanged,
		ReasonProcessObservationIncomplete, ReasonZellijCatalogUnavailable, ReasonAuthorityUnavailable, ReasonRevisionOverflow:
		return true
	default:
		return false
	}
}

func validConflict(code ConflictCode) bool {
	switch code {
	case ConflictKittyProcessUnverified, ConflictProcessMetadataInvalid, ConflictSessionCandidateMissing,
		ConflictSessionCandidateAmbiguous, ConflictSessionNameInvalid, ConflictSessionMissing,
		ConflictSessionDeadResurrectable, ConflictSessionPrefixOnly, ConflictSessionCatalogDuplicate,
		ConflictSessionDuplicateBinding, ConflictSessionSocketInvalid, ConflictWorkspaceNameCollision:
		return true
	default:
		return false
	}
}
