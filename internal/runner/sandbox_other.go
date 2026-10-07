//go:build !linux

package runner

import "os/exec"

// setProcGroup is a no-op outside Linux.
func setProcGroup(cmd *exec.Cmd) {}
