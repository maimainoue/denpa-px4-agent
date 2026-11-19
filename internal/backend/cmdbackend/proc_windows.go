//go:build windows

package cmdbackend

import (
	"os"
	"os/exec"
)

// Windows ではシグナルで止められないので、最初から強制終了する。
// 仕様書 17.1 は FAKE_TUNE のためにジョブオブジェクトでの停止を求めている。孫プロセスまでは
// まだ止めない (TODO: ジョブオブジェクト)。

func setProcAttr(cmd *exec.Cmd) {}

func terminateProc(p *os.Process) { _ = p.Kill() }

func killProc(p *os.Process) { _ = p.Kill() }
