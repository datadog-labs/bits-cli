//go:build !windows

package scripts

import "testing"

// isolateUserPath has nothing to do: install.sh only prints PATH advice.
func isolateUserPath(*testing.T) {}
