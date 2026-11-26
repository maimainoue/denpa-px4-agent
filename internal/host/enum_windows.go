//go:build windows

package host

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// setupapi.dll は System32 から読む (エージェントの隣やカレントのものは読まない)。
var (
	setupapi                     = windows.NewLazySystemDLL("setupapi.dll")
	procGetClassDevs             = setupapi.NewProc("SetupDiGetClassDevsW")
	procEnumDeviceInterfaces     = setupapi.NewProc("SetupDiEnumDeviceInterfaces")
	procGetDeviceInterfaceDetail = setupapi.NewProc("SetupDiGetDeviceInterfaceDetailW")
	procDestroyDeviceInfoList    = setupapi.NewProc("SetupDiDestroyDeviceInfoList")
	guidDevinterfaceUSBDevice    = windows.GUID{Data1: 0xA5DCBF10, Data2: 0x6530, Data3: 0x11D2, Data4: [8]byte{0x90, 0x1F, 0x00, 0xC0, 0x4F, 0xB9, 0x51, 0xED}}
)

const (
	digcfPresent         = 0x2
	digcfDeviceInterface = 0x10
)

// spDeviceInterfaceData は SP_DEVICE_INTERFACE_DATA。
type spDeviceInterfaceData struct {
	cbSize             uint32
	interfaceClassGUID windows.GUID
	flags              uint32
	reserved           uintptr
}

// NewEnumerator は SetupAPI で数える Enumerator。
func NewEnumerator() Enumerator { return setupEnum{} }

type setupEnum struct{}

func (setupEnum) InterfacePaths(guid string) ([]string, error) {
	g, err := windows.GUIDFromString(guid)
	if err != nil {
		return nil, fmt.Errorf("GUID を読めません (%s): %w", guid, err)
	}
	return interfacePaths(&g)
}

func (setupEnum) USBDevices() ([]string, error) { return interfacePaths(&guidDevinterfaceUSBDevice) }

// interfacePaths は、挿さっているデバイスインターフェースのデバイスパス (小文字) を並べる。
func interfacePaths(g *windows.GUID) ([]string, error) {
	h, _, e := procGetClassDevs.Call(uintptr(unsafe.Pointer(g)), 0, 0, digcfPresent|digcfDeviceInterface)
	if h == uintptr(windows.InvalidHandle) {
		return nil, fmt.Errorf("SetupDiGetClassDevsW: %w", e)
	}
	defer procDestroyDeviceInfoList.Call(h)

	var out []string
	for i := uint32(0); ; i++ {
		var data spDeviceInterfaceData
		data.cbSize = uint32(unsafe.Sizeof(data))
		r, _, e := procEnumDeviceInterfaces.Call(h, 0, uintptr(unsafe.Pointer(g)), uintptr(i), uintptr(unsafe.Pointer(&data)))
		if r == 0 {
			if e == windows.ERROR_NO_MORE_ITEMS {
				return out, nil
			}
			return out, fmt.Errorf("SetupDiEnumDeviceInterfaces: %w", e)
		}
		// 必要な長さは、ERROR_INSUFFICIENT_BUFFER のときだけ返る。列挙直後の抜去などで別のエラーなら、その機材を飛ばす
		var size uint32
		_, _, e = procGetDeviceInterfaceDetail.Call(h, uintptr(unsafe.Pointer(&data)), 0, 0, uintptr(unsafe.Pointer(&size)), 0)
		if e != windows.ERROR_INSUFFICIENT_BUFFER || size < 6 {
			continue
		}
		// SP_DEVICE_INTERFACE_DETAIL_DATA_W: 先頭 4 バイトが cbSize (固定部分の大きさ。64 ビットは 8、32 ビットは 6)、その後ろがパス
		buf := make([]uint32, (size+3)/4)
		buf[0] = 8
		if unsafe.Sizeof(uintptr(0)) == 4 {
			buf[0] = 6
		}
		r, _, _ = procGetDeviceInterfaceDetail.Call(h, uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&buf[0])), uintptr(size), 0, 0)
		if r == 0 {
			continue
		}
		path := windows.UTF16PtrToString((*uint16)(unsafe.Add(unsafe.Pointer(&buf[0]), 4)))
		out = append(out, strings.ToLower(path))
	}
}
