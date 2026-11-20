//go:build !windows

package scripts

import "testing"

// keepUserPath has nothing to restore: install.sh only prints PATH advice.
func keepUserPath(*testing.T) {}
