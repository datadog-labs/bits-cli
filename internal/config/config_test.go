package config

import (
	"strings"
	"testing"
)

// withEnv replaces the environment Load reads for one test.
func withEnv(t *testing.T, env map[string]string) {
	t.Helper()
	old := getEnv
	getEnv = func(name string) string { return env[name] }
	t.Cleanup(func() { getEnv = old })
}

func TestLoadDefaults(t *testing.T) {
	withEnv(t, map[string]string{EnvConfigDir: "/tmp/bits"})
	c, err := Load(Params{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Model != "" || c.InferenceMode != "" || c.CustomHeaders != nil ||
		c.NoUpdateCheck || c.FakeBackend || c.APIKey() != "" || c.AppKey() != "" || c.Dir != "/tmp/bits" {
		t.Fatalf("Load() = %#v, want zero values", c)
	}
}

func TestLoadFromEnv(t *testing.T) {
	withEnv(t, map[string]string{
		EnvModel:         "env-model",
		EnvInferenceMode: "deep",
		EnvCustomHeaders: "x-route-hint=1",
		EnvNoUpdateCheck: "true",
		EnvFakeBackend:   "1",
		EnvAPIKey:        "api-key",
		EnvAppKey:        "app-key",
	})
	c, err := Load(Params{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Model != "env-model" || c.InferenceMode != "deep" {
		t.Errorf("Model, InferenceMode = %q, %q", c.Model, c.InferenceMode)
	}
	if got := c.CustomHeaders.Get("x-route-hint"); got != "1" {
		t.Errorf("CustomHeaders = %v", c.CustomHeaders)
	}
	if !c.NoUpdateCheck || !c.FakeBackend {
		t.Errorf("NoUpdateCheck, FakeBackend = %v, %v", c.NoUpdateCheck, c.FakeBackend)
	}
	if c.APIKey() != "api-key" || c.AppKey() != "app-key" {
		t.Errorf("APIKey, AppKey = %q, %q", c.APIKey(), c.AppKey())
	}
}

func TestLoadFlagsOverrideEnv(t *testing.T) {
	withEnv(t, map[string]string{EnvModel: "env-model", EnvInferenceMode: "deep"})
	c, err := Load(Params{Model: "flag-model", InferenceMode: "fast"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Model != "flag-model" || c.InferenceMode != "fast" {
		t.Fatalf("Model, InferenceMode = %q, %q; want the flag values", c.Model, c.InferenceMode)
	}
}

func TestLoadClientID(t *testing.T) {
	for _, test := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "staging", env: map[string]string{EnvStagingClientID: "staging"}, want: "staging"},
		{name: "both", env: map[string]string{EnvClientID: "client", EnvStagingClientID: "staging"}, want: "client"},
	} {
		t.Run(test.name, func(t *testing.T) {
			withEnv(t, test.env)
			c, err := Load(Params{})
			if err != nil {
				t.Fatal(err)
			}
			if c.ClientID != test.want {
				t.Fatalf("ClientID = %q, want %q", c.ClientID, test.want)
			}
		})
	}
}

func TestLoadRejectsMalformedEnv(t *testing.T) {
	for _, test := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "inference mode", env: map[string]string{EnvInferenceMode: "normal"}, want: "invalid inference mode"},
		{name: "custom headers", env: map[string]string{EnvCustomHeaders: "bad name=1"}, want: EnvCustomHeaders},
		{name: "update check", env: map[string]string{EnvNoUpdateCheck: "yes"}, want: EnvNoUpdateCheck},
		{name: "fake backend", env: map[string]string{EnvFakeBackend: "on"}, want: EnvFakeBackend},
		{name: "relative config dir", env: map[string]string{EnvConfigDir: "relative/dir"}, want: EnvConfigDir},
	} {
		t.Run(test.name, func(t *testing.T) {
			withEnv(t, test.env)
			if c, err := Load(Params{}); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() = %#v, %v; want an error naming %q", c, err, test.want)
			}
		})
	}
}
