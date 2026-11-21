//go:build linux

package cardshare

import (
	"encoding/binary"
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

// measureRTT は OS が TCP の ACK から測っている往復時間を返します。
// Linux の TCP_INFO の tcpi_rtt (マイクロ秒) を読みます。
// struct tcp_info の先頭 68 バイトは u8 と u32 だけなので、
// tcpi_rtt はどの CPU でも offset 68 にあります。
func measureRTT(c syscall.RawConn) (time.Duration, error) {
	var rtt time.Duration
	var opErr error
	err := c.Control(func(fd uintptr) {
		var info [128]byte
		length := uint32(len(info))
		_, _, errno := syscall.Syscall6(syscall.SYS_GETSOCKOPT, fd,
			uintptr(syscall.IPPROTO_TCP), uintptr(syscall.TCP_INFO),
			uintptr(unsafe.Pointer(&info[0])), uintptr(unsafe.Pointer(&length)), 0)
		if errno != 0 {
			opErr = errno
			return
		}
		if length < 72 {
			opErr = fmt.Errorf("TCP_INFO が短すぎます (%d バイト)", length)
			return
		}
		rtt = time.Duration(binary.LittleEndian.Uint32(info[68:72])) * time.Microsecond
	})
	if err != nil {
		return 0, err
	}
	if opErr != nil {
		return 0, opErr
	}
	return rtt, nil
}
