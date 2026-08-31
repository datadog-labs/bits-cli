package auth

// Mode selects the authentication policy for an Assistant client.
type Mode string

const (
	// ModeAuto uses a stored OAuth session.
	ModeAuto Mode = "auto"
	// ModeAPIKey requires explicit API/app-key authentication.
	ModeAPIKey Mode = "api-key"
)
