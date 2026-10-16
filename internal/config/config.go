// Package config resolves the Bits CLI configuration of one invocation from
// its flags and environment. It is the only package that reads configuration
// from the environment; the rest of the CLI receives resolved values. It is a
// leaf: any package may import it.
package config

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// Environment variables read by Load.
const (
	// EnvConfigDir overrides the per-user config directory; it must be absolute.
	EnvConfigDir = "BITS_CONFIG_DIR"
	// EnvModel is the backend model ID; --model overrides it.
	EnvModel = "BITS_ASSISTANT_MODEL"
	// EnvInferenceMode is the reasoning mode, fast or deep; --reasoning
	// overrides it.
	EnvInferenceMode = "BITS_ASSISTANT_INFERENCE_MODE"
	// EnvNoUpdateCheck disables the startup update check.
	EnvNoUpdateCheck = "BITS_NO_UPDATE_CHECK"
	// EnvFakeBackend replaces the Assistant API with the scripted fake backend.
	EnvFakeBackend = "BITS_FAKE_BACKEND"
	// EnvCustomHeaders carries extra request headers as a comma-separated list
	// of name=value pairs, applied to every Assistant API request:
	//
	//	BITS_CUSTOM_HEADERS='x-route-hint=1,x-trace-me=abc'
	//
	// It is deliberately an environment variable rather than a flag: the
	// mechanism is for development and support, and is not part of the
	// command-line contract. See parseCustomHeaders for the syntax.
	EnvCustomHeaders = "BITS_CUSTOM_HEADERS"
	// EnvClientID and EnvStagingClientID set the OAuth client for login;
	// EnvClientID wins and --client-id overrides both.
	EnvClientID        = "BITS_CLIENT_ID"
	EnvStagingClientID = "BITS_STAGING_CLIENT_ID"
	// EnvAPIKey and EnvAppKey are the credentials for --auth api-key.
	EnvAPIKey = "DD_API_KEY"
	EnvAppKey = "DD_APP_KEY"
)

var getEnv = os.Getenv

// Params holds the flag values that the environment can also set. A flag left
// empty falls back to its environment variable.
type Params struct {
	Model         string
	InferenceMode string
}

// Config is the resolved configuration of one invocation. Load builds it
// before any command work, and it is read-only afterwards.
type Config struct {
	// ClientID is the OAuth client for login; empty selects the site's
	// built-in client.
	ClientID string

	Model         string
	InferenceMode string // "", "fast" or "deep"
	CustomHeaders http.Header

	NoUpdateCheck bool
	FakeBackend   bool

	// Dir is the per-user config directory; Load does not create it.
	Dir string

	// apiKey and appKey are private so a printed or serialized configuration
	// never carries them.
	apiKey string
	appKey string
}

// APIKey returns the DD_API_KEY credential for --auth api-key.
func (c *Config) APIKey() string { return c.apiKey }

// AppKey returns the DD_APP_KEY credential for --auth api-key.
func (c *Config) AppKey() string { return c.appKey }

// Load resolves the configuration from params and the environment.
func Load(params Params) (*Config, error) {
	inferenceMode, err := parseInferenceMode(coalesce(params.InferenceMode, getEnv(EnvInferenceMode)))
	if err != nil {
		return nil, err
	}
	// Parsed before any authentication, so a malformed value is reported as
	// itself rather than behind a login prompt.
	customHeaders, err := parseCustomHeaders(getEnv(EnvCustomHeaders))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvCustomHeaders, err)
	}
	noUpdateCheck, err := getEnvBool(EnvNoUpdateCheck)
	if err != nil {
		return nil, err
	}
	fakeBackend, err := getEnvBool(EnvFakeBackend)
	if err != nil {
		return nil, err
	}
	dir, err := loadDir()
	if err != nil {
		return nil, err
	}
	return &Config{
		ClientID:      coalesce(getEnv(EnvClientID), getEnv(EnvStagingClientID)),
		Model:         coalesce(params.Model, getEnv(EnvModel)),
		InferenceMode: inferenceMode,
		CustomHeaders: customHeaders,
		NoUpdateCheck: noUpdateCheck,
		FakeBackend:   fakeBackend,
		Dir:           dir,
		apiKey:        getEnv(EnvAPIKey),
		appKey:        getEnv(EnvAppKey),
	}, nil
}

func parseInferenceMode(raw string) (string, error) {
	switch raw {
	case "", "fast", "deep":
		return raw, nil
	default:
		return "", fmt.Errorf("invalid inference mode %q; expected fast or deep", raw)
	}
}

func getEnvBool(name string) (bool, error) {
	raw := strings.TrimSpace(getEnv(name))
	if raw == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s: invalid boolean %q", name, raw)
	}
	return v, nil
}

// coalesce returns the first nonempty value.
func coalesce(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
