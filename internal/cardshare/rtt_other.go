//go:build !linux && !darwin && !windows

package cardshare

import (
	"fmt"
	"syscall"
	"time"
)

// measureRTT は Linux・macOS・Windows 以外ではまだ測れません。
func measureRTT(c syscall.RawConn) (time.Duration, error) {
	return 0, fmt.Errorf("往復時間はこの OS では見られません (Linux・macOS・Windows だけです)")
}
