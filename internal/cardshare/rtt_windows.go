//go:build windows

package cardshare

import (
	"encoding/binary"
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

// SIO_TCP_INFO は WSAIoctl で TCP の統計を取る制御コードです。
// _WSAIORW(IOC_VENDOR, 39) = 0xC0000000 | 0x18000000 | 39 です。
// 入力は DWORD の版 (0 が TCP_INFO_v0)、出力は TCP_INFO_v0 です。
// TCP_INFO_v0 は State (4)・Mss (4)・ConnectionTimeMs (8)・
// TimestampsEnabled (1)・(詰め物 3)・RttUs (4)…なので、
// RttUs は offset 20 にあり、単位はマイクロ秒です。
const sioTCPInfo = 0xD8000027

var (
	ws2_32       = syscall.NewLazyDLL("ws2_32.dll")
	procWSAIoctl = ws2_32.NewProc("WSAIoctl")
)

// measureRTT は OS が TCP の ACK から測っている往復時間を返します。
// Windows の SIO_TCP_INFO の TCP_INFO_v0.RttUs (マイクロ秒) を読みます。
func measureRTT(c syscall.RawConn) (time.Duration, error) {
	var rtt time.Duration
	var opErr error
	err := c.Control(func(fd uintptr) {
		version := uint32(0)
		var info [64]byte
		var returned uint32
		ret, _, _ := procWSAIoctl.Call(
			fd,
			uintptr(sioTCPInfo),
			uintptr(unsafe.Pointer(&version)),
			uintptr(unsafe.Sizeof(version)),
			uintptr(unsafe.Pointer(&info[0])),
			uintptr(len(info)),
			uintptr(unsafe.Pointer(&returned)),
			0, 0,
		)
		if ret != 0 {
			opErr = fmt.Errorf("WSAIoctl (SIO_TCP_INFO) が失敗しました")
			return
		}
		if returned < 24 {
			opErr = fmt.Errorf("TCP_INFO_v0 が短すぎます (%d バイト)", returned)
			return
		}
		rtt = time.Duration(binary.LittleEndian.Uint32(info[20:24])) * time.Microsecond
	})
	if err != nil {
		return 0, err
	}
	if opErr != nil {
		return 0, opErr
	}
	return rtt, nil
}
