package filediff

// SnapshotState describes whether text was safely captured for a diff.
type SnapshotState uint8

const (
	SnapshotReady SnapshotState = iota
	SnapshotMissing
	SnapshotUnavailable
)

// Snapshot is a normalized, display-safe filesystem capture supplied by the
// tools package. filediff never performs filesystem I/O itself.
type Snapshot struct {
	Path string
	// Raw is the exact safely-read file representation. Text is normalized for
	// edit matching; write rendering may use Raw to preserve BOM/line endings.
	Raw    string
	Text   string
	State  SnapshotState
	Reason string
}

// Phase distinguishes incomplete input from a completed/applied mutation.
type Phase uint8

const (
	PhaseStreaming Phase = iota
	PhaseReady
	PhaseApplied
	PhaseUnavailable
)

// PreviewKind identifies the tool-specific meaning of an incomplete preview.
type PreviewKind uint8

const (
	PreviewWritePrefix PreviewKind = iota + 1
	PreviewEdit
)

// Preview is presentation data for a speculative change. Pending records that
// more streamed input is expected; it is reducer state, not a rendered row.
type Preview struct {
	Kind PreviewKind
	Diff *Diff
	// InputPrefixBytes and InputPrefixHash identify the visible streamed input
	// that produced this immutable preview. They avoid retaining a second raw
	// input copy while allowing tools to preserve the state between incomplete
	// logical lines.
	InputPrefixBytes int
	InputPrefixHash  uint64
	Pending          bool
	InputTruncated   bool
}

// Op identifies the mutation represented by a final change.
type Op uint8

const (
	OpCreate Op = iota + 1
	OpOverwrite
	OpEdit
)

// ChangeState describes whether the handler applied the represented change.
type ChangeState uint8

const (
	ChangeApplied ChangeState = iota + 1
	ChangeUnavailable
)

// Change is a handler-authoritative mutation result. The complete persisted
// unified diff also belongs in the tool Display field.
type Change struct {
	Path  string
	Op    Op
	State ChangeState
	Diff  *Diff
}

// State is the immutable concrete value transported through agent.RenderState.
type State struct {
	Phase    Phase
	Snapshot *Snapshot
	Preview  *Preview
	Change   *Change
	Reason   string
}
