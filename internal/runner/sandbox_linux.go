package runner

import (
	"os/exec"
	"syscall"
)

// setProcGroup puts cmd in its own process group so a terminal SIGINT (sent
// to the foreground process group) reaches only nightshift, not its sbx
// children.
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
