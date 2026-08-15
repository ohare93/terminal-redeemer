package sliceprotocol

import "time"

const (
	SchemaVersion          uint32 = 2
	MaxPayloadBytes               = 4 << 20
	MaxStringBytes                = 1024
	WorkspaceNormalization        = "unicode-nfkc-fold-v1"
)

type Quality string

const (
	QualityComplete Quality = "complete"
	QualityDegraded Quality = "degraded"
)

type ReasonCode string

const (
	ReasonNiriSocketUnavailable        ReasonCode = "niri_socket_unavailable"
	ReasonNiriReplayTimeout            ReasonCode = "niri_replay_timeout"
	ReasonNiriReplayEOF                ReasonCode = "niri_replay_eof"
	ReasonNiriConfigFailed             ReasonCode = "niri_config_failed"
	ReasonNiriMalformed                ReasonCode = "niri_malformed"
	ReasonNiriReplyTooLarge            ReasonCode = "niri_reply_too_large"
	ReasonNiriMissingWorkspace         ReasonCode = "niri_missing_workspace"
	ReasonNiriMissingOutput            ReasonCode = "niri_missing_output"
	ReasonNiriInvalidGeometry          ReasonCode = "niri_invalid_geometry"
	ReasonNiriUnsupportedTopology      ReasonCode = "niri_unsupported_topology"
	ReasonSourceIdentityChanged        ReasonCode = "source_identity_changed"
	ReasonProcessObservationIncomplete ReasonCode = "process_observation_incomplete"
	ReasonZellijCatalogUnavailable     ReasonCode = "zellij_catalog_unavailable"
	ReasonAuthorityUnavailable         ReasonCode = "authority_unavailable"
	ReasonRevisionOverflow             ReasonCode = "revision_overflow"
)

type ConflictCode string

const (
	ConflictKittyProcessUnverified    ConflictCode = "kitty_process_unverified"
	ConflictProcessMetadataInvalid    ConflictCode = "process_metadata_invalid"
	ConflictSessionCandidateMissing   ConflictCode = "session_candidate_missing"
	ConflictSessionCandidateAmbiguous ConflictCode = "session_candidate_ambiguous"
	ConflictSessionNameInvalid        ConflictCode = "session_name_invalid"
	ConflictSessionMissing            ConflictCode = "session_missing"
	ConflictSessionDeadResurrectable  ConflictCode = "session_dead_resurrectable"
	ConflictSessionPrefixOnly         ConflictCode = "session_prefix_only"
	ConflictSessionCatalogDuplicate   ConflictCode = "session_catalog_duplicate"
	ConflictSessionDuplicateBinding   ConflictCode = "session_duplicate_binding"
	ConflictSessionSocketInvalid      ConflictCode = "session_socket_invalid"
	ConflictWorkspaceNameCollision    ConflictCode = "workspace_name_collision"
)

type Envelope struct {
	SchemaVersion uint32         `json:"schema_version"`
	SourceHostID  string         `json:"source_host_id"`
	Observation   Observation    `json:"observation"`
	Authoritative *Authoritative `json:"authoritative,omitempty"`
}

type Observation struct {
	Quality         Quality   `json:"quality"`
	AttemptedAt     time.Time `json:"attempted_at"`
	DegradedReasons []Reason  `json:"degraded_reasons,omitempty"`
}

type Reason struct {
	Code ReasonCode `json:"code"`
}

type Authoritative struct {
	SourceEpoch            string     `json:"source_epoch"`
	Revision               uint64     `json:"revision"`
	ObservedAt             time.Time  `json:"observed_at"`
	WorkspaceNormalization string     `json:"workspace_normalization"`
	LiveSessionIDs         []string   `json:"live_session_ids"`
	Sources                []Source   `json:"sources"`
	Conflicts              []Conflict `json:"conflicts"`
}

type Source struct {
	SourceID        string    `json:"source_id"`
	RuntimeWindowID uint64    `json:"runtime_window_id"`
	Session         Session   `json:"session"`
	Workspace       Workspace `json:"workspace"`
	Output          *Output   `json:"output,omitempty"`
	Layout          Layout    `json:"layout"`
}

type Session struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type Workspace struct {
	RuntimeID uint64 `json:"runtime_id"`
	Name      string `json:"name,omitempty"`
	Key       string `json:"key,omitempty"`
}

type Output struct {
	Name          string  `json:"name"`
	LogicalX      int     `json:"logical_x"`
	LogicalY      int     `json:"logical_y"`
	LogicalWidth  int     `json:"logical_width"`
	LogicalHeight int     `json:"logical_height"`
	Scale         float64 `json:"scale"`
	Transform     string  `json:"transform"`
}

type Position struct {
	Column int `json:"column"`
	Tile   int `json:"tile"`
}

type Layout struct {
	Mode         string    `json:"mode"`
	Position     *Position `json:"position,omitempty"`
	TileWidth    float64   `json:"tile_width"`
	TileHeight   float64   `json:"tile_height"`
	WindowWidth  int       `json:"window_width"`
	WindowHeight int       `json:"window_height"`
}

type Conflict struct {
	Code      ConflictCode `json:"code"`
	SourceID  string       `json:"source_id,omitempty"`
	SessionID string       `json:"session_id,omitempty"`
}

type VersionError struct {
	Code                    string   `json:"code"`
	SupportedSchemaVersions []uint32 `json:"supported_schema_versions"`
}
