//go:build !linux && !darwin

package tools

import "github.com/DataDog/bits-cli/internal/agent"

func platformExecTools(string) []agent.Tool { return nil }
