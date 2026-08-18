package assistant

// String returns a short, stable label for a ContentKind, used for logging and
// as the fallback render label. The switch is exhaustive so a new kind must be
// named here.
//
//exhaustive:enforce
func (k ContentKind) String() string {
	switch k {
	case KindText:
		return "text"
	case KindReasoning:
		return "reasoning"
	case KindToolCall:
		return "tool_call"
	case KindToolResult:
		return "tool_result"
	case KindWidget:
		return "widget"
	case KindDashboard:
		return "dashboard"
	case KindProgress:
		return "progress"
	case KindTurnMarker:
		return "turn"
	case KindStop:
		return "stop"
	case KindInternal:
		return "internal"
	case KindUnknown:
		return "unknown"
	}
	return "unknown"
}
