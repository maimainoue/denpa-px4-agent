//go:build darwin

package cardshare

import (
	"encoding/binary"
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

// tcpConnectionInfo は macOS の TCP_CONNECTION_INFO (0x106) です。
// XNU の bsd/netinet/tcp.h の struct tcp_connection_info を見ています。
// tcpi_rttcur は「直近の往復時間 (ミリ秒)」で、offset 40 にあります。
// 1 ms 刻みなので、0 は 1 ms 未満ということです。
const tcpConnectionInfo = 0x106

// measureRTT は OS が TCP の ACK から測っている往復時間を返します。
// macOS の TCP_CONNECTION_INFO の tcpi_rttcur (ミリ秒) を読みます。
func measureRTT(c syscall.RawConn) (time.Duration, error) {
	var rtt time.Duration
	var opErr error
	err := c.Control(func(fd uintptr) {
		var info [64]byte
		length := uint32(len(info))
		_, _, errno := syscall.Syscall6(syscall.SYS_GETSOCKOPT, fd,
			uintptr(syscall.IPPROTO_TCP), uintptr(tcpConnectionInfo),
			uintptr(unsafe.Pointer(&info[0])), uintptr(unsafe.Pointer(&length)), 0)
		if errno != 0 {
			opErr = errno
			return
		}
		if length < 44 {
			opErr = fmt.Errorf("TCP_CONNECTION_INFO が短すぎます (%d バイト)", length)
			return
		}
		rtt = time.Duration(binary.LittleEndian.Uint32(info[40:44])) * time.Millisecond
	})
	if err != nil {
		return 0, err
	}
	if opErr != nil {
		return 0, opErr
	}
	return rtt, nil
}
