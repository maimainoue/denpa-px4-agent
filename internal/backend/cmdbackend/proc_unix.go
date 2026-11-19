//go:build !windows

package cmdbackend

import (
	"os"
	"os/exec"
	"syscall"
)

// 子プロセスを自分のプロセスグループに置き、止めるときはグループごとに送る (仕様書 6)。

func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateProc(p *os.Process) {
	_ = syscall.Kill(-p.Pid, syscall.SIGTERM)
}

func killProc(p *os.Process) {
	_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
}
