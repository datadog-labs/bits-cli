package assistant

// Role is the message author. The wire form is the string Message.Role
// ("user" / "assistant"); Role is the typed value the rest of the app uses.
type Role int

const (
	RoleUser Role = iota
	RoleAssistant
	RoleSystem
)

// RoleOf maps a wire role string to a Role. Unknown values fall back to
// RoleAssistant.
func RoleOf(s string) Role {
	switch s {
	case "user":
		return RoleUser
	case "system":
		return RoleSystem
	default:
		return RoleAssistant
	}
}
