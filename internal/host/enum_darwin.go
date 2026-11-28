//go:build darwin

package host

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// IOKit と CoreFoundation を、cgo なしで purego から呼ぶ (PCSC.framework と同じ作り方。仕様書 5.3)。
// 挿さっている USB デバイスは、IORegistry の IOUSBHostDevice (macOS 11 以降) を `idVendor`・`idProduct` で読む。
// 互換用の AppleUSBDevice (IOUSBDevice) は、同じ機材が二重に出るので使わない。
type iokit struct {
	matching   func(name *byte) uintptr
	getMatch   func(master uint32, matching uintptr, it *uint32) int32
	next       func(it uint32) uint32
	release    func(obj uint32) int32
	createProp func(entry uint32, key uintptr, alloc uintptr, opts uint32) uintptr

	cfString    func(alloc uintptr, s *byte, enc uint32) uintptr
	cfRelease   func(cf uintptr)
	cfTypeID    func(cf uintptr) uint
	cfNumberID  func() uint
	cfNumberVal func(n uintptr, typ int, out unsafe.Pointer) bool
}

const (
	kCFStringEncodingUTF8 = 0x08000100
	kCFNumberSInt32Type   = 3
)

var (
	iokitOnce sync.Once
	iokitAPI  *iokit
	iokitErr  error
)

func loadIOKit() (*iokit, error) {
	iokitOnce.Do(func() {
		io, err := purego.Dlopen("/System/Library/Frameworks/IOKit.framework/IOKit", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			iokitErr = err
			return
		}
		cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			iokitErr = err
			return
		}
		k := &iokit{}
		for _, e := range []struct {
			fn   any
			lib  uintptr
			name string
		}{
			{&k.matching, io, "IOServiceMatching"},
			{&k.getMatch, io, "IOServiceGetMatchingServices"},
			{&k.next, io, "IOIteratorNext"},
			{&k.release, io, "IOObjectRelease"},
			{&k.createProp, io, "IORegistryEntryCreateCFProperty"},
			{&k.cfString, cf, "CFStringCreateWithCString"},
			{&k.cfRelease, cf, "CFRelease"},
			{&k.cfTypeID, cf, "CFGetTypeID"},
			{&k.cfNumberID, cf, "CFNumberGetTypeID"},
			{&k.cfNumberVal, cf, "CFNumberGetValue"},
		} {
			if err := registerFunc(e.fn, e.lib, e.name); err != nil {
				iokitErr = err
				return
			}
		}
		iokitAPI = k
	})
	return iokitAPI, iokitErr
}

// registerFunc は purego の RegisterLibFunc。関数が無いときは panic でなくエラーにする。
func registerFunc(fn any, lib uintptr, name string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%s を読めません: %v", name, r)
		}
	}()
	purego.RegisterLibFunc(fn, lib, name)
	return nil
}

func cstr(s string) *byte {
	b := append([]byte(s), 0)
	return &b[0]
}

// NewEnumerator は IOKit で数える Enumerator。
func NewEnumerator() Enumerator { return iokitEnum{} }

type iokitEnum struct{}

// InterfacePaths: macOS にはデバイスインターフェースの GUID が無い (DriverHost_PX4 は libusb で機材を見る)。
func (iokitEnum) InterfacePaths(string) ([]string, error) { return nil, ErrNoInterface }

// USBDevices は IOUSBHostDevice を並べて、`vid_xxxx&pid_xxxx` を含む文字列にして返す。
func (iokitEnum) USBDevices() ([]string, error) {
	k, err := loadIOKit()
	if err != nil {
		return nil, err
	}
	dict := k.matching(cstr("IOUSBHostDevice"))
	if dict == 0 {
		return nil, errors.New("IOServiceMatching に失敗しました")
	}
	var it uint32
	// matching の参照は IOServiceGetMatchingServices が引き取る (こちらで CFRelease しない)。0 は kIOMasterPortDefault
	if kr := k.getMatch(0, dict, &it); kr != 0 {
		return nil, fmt.Errorf("IOServiceGetMatchingServices: 0x%x", uint32(kr))
	}
	defer k.release(it)

	keyV := k.cfString(0, cstr("idVendor"), kCFStringEncodingUTF8)
	keyP := k.cfString(0, cstr("idProduct"), kCFStringEncodingUTF8)
	if keyV == 0 || keyP == 0 {
		return nil, errors.New("CFStringCreateWithCString に失敗しました")
	}
	defer k.cfRelease(keyV)
	defer k.cfRelease(keyP)

	number := func(entry uint32, key uintptr) (int32, bool) {
		v := k.createProp(entry, key, 0, 0)
		if v == 0 {
			return 0, false
		}
		defer k.cfRelease(v)
		if k.cfTypeID(v) != k.cfNumberID() {
			return 0, false
		}
		var n int32
		if !k.cfNumberVal(v, kCFNumberSInt32Type, unsafe.Pointer(&n)) {
			return 0, false
		}
		return n, true
	}

	var out []string
	for i := 0; ; i++ {
		entry := k.next(it)
		if entry == 0 {
			return out, nil
		}
		vid, ok1 := number(entry, keyV)
		pid, ok2 := number(entry, keyP)
		k.release(entry)
		if ok1 && ok2 {
			out = append(out, fmt.Sprintf("iokit#vid_%04x&pid_%04x#%d", vid, pid, i))
		}
	}
}
