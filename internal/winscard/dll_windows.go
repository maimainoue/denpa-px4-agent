//go:build windows

package winscard

import (
	"errors"
	"fmt"
	"path/filepath"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DefaultDLL は System32 の winscard.dll。WinSCard_PX4 の互換 DLL は、エージェントの隣に置いても読まない。
func DefaultDLL() string {
	d, err := windows.GetSystemDirectory()
	if err != nil || d == "" {
		d = `C:\Windows\System32`
	}
	return filepath.Join(d, "winscard.dll")
}

const (
	scardScopeSystem = 2
	scardShareExcl   = 1
	scardProtoT1     = 2
	scardLeaveCard   = 0
	maxRecv          = 1024 // 短い APDU の応答 (最大 256 + SW) に十分
)

// dll は読んだ DLL。関数のアドレスは読むときに全部引いておく。
type dll struct {
	h                                                              windows.Handle
	establish, release, listReaders, connect, transmit, disconnect uintptr
}

// LoadDLL は絶対パスの DLL を読む。DLL の探し場所は、そのファイルの隣とシステムの場所だけに絞る
// (カレントディレクトリや PATH から差し替えられない)。
func LoadDLL(path string) (API, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("絶対パスにします (%s)", path)
	}
	h, err := windows.LoadLibraryEx(path, 0, windows.LOAD_LIBRARY_SEARCH_DEFAULT_DIRS|windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR)
	if err != nil {
		return nil, err
	}
	d := &dll{h: h}
	for _, f := range []struct {
		p    *uintptr
		name string
	}{
		{&d.establish, "SCardEstablishContext"},
		{&d.release, "SCardReleaseContext"},
		{&d.listReaders, "SCardListReadersW"},
		{&d.connect, "SCardConnectW"},
		{&d.transmit, "SCardTransmit"},
		{&d.disconnect, "SCardDisconnect"},
	} {
		addr, err := windows.GetProcAddress(h, f.name)
		if err != nil {
			windows.FreeLibrary(h)
			return nil, fmt.Errorf("%s がありません: %w", f.name, err)
		}
		*f.p = addr
	}
	return d, nil
}

func rv(op string, r uintptr) error {
	if uint32(r) == RVOK {
		return nil
	}
	return &Error{Op: op, RV: uint32(r)}
}

func (d *dll) EstablishContext() (uintptr, error) {
	var ctx uintptr
	r, _, _ := syscall.SyscallN(d.establish, scardScopeSystem, 0, 0, uintptr(unsafe.Pointer(&ctx)))
	if err := rv("SCardEstablishContext", r); err != nil {
		return 0, err
	}
	return ctx, nil
}

func (d *dll) ReleaseContext(ctx uintptr) { syscall.SyscallN(d.release, ctx) }

func (d *dll) ListReaders(ctx uintptr) ([]string, error) {
	var n uint32
	r, _, _ := syscall.SyscallN(d.listReaders, ctx, 0, 0, uintptr(unsafe.Pointer(&n)))
	if uint32(r) == RVNoReaders {
		return nil, nil
	}
	if err := rv("SCardListReaders", r); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	buf := make([]uint16, n)
	r, _, _ = syscall.SyscallN(d.listReaders, ctx, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if uint32(r) == RVNoReaders {
		return nil, nil
	}
	if err := rv("SCardListReaders", r); err != nil {
		return nil, err
	}
	if int(n) > len(buf) {
		n = uint32(len(buf))
	}
	return splitMulti(buf[:n]), nil
}

// splitMulti は NUL 区切り・NUL 2 つで終わる文字列の並びを分ける。
func splitMulti(b []uint16) []string {
	var out []string
	start := 0
	for i, c := range b {
		if c != 0 {
			continue
		}
		if i > start {
			out = append(out, string(utf16.Decode(b[start:i])))
		}
		start = i + 1
	}
	return out
}

func (d *dll) Connect(ctx uintptr, reader string) (uintptr, uint32, error) {
	name, err := windows.UTF16PtrFromString(reader)
	if err != nil {
		return 0, 0, err
	}
	var h uintptr
	var proto uint32
	r, _, _ := syscall.SyscallN(d.connect, ctx, uintptr(unsafe.Pointer(name)), scardShareExcl, scardProtoT1,
		uintptr(unsafe.Pointer(&h)), uintptr(unsafe.Pointer(&proto)))
	if err := rv("SCardConnect", r); err != nil {
		return 0, 0, err
	}
	return h, proto, nil
}

// scardIORequest は SCARD_IO_REQUEST。
type scardIORequest struct {
	Protocol  uint32
	PciLength uint32
}

func (d *dll) Transmit(card uintptr, proto uint32, apdu []byte) ([]byte, error) {
	if len(apdu) == 0 {
		return nil, errors.New("APDU が空です")
	}
	pci := scardIORequest{Protocol: proto, PciLength: uint32(unsafe.Sizeof(scardIORequest{}))}
	recv := make([]byte, maxRecv)
	n := uint32(len(recv))
	r, _, _ := syscall.SyscallN(d.transmit, card, uintptr(unsafe.Pointer(&pci)),
		uintptr(unsafe.Pointer(&apdu[0])), uintptr(len(apdu)),
		0, uintptr(unsafe.Pointer(&recv[0])), uintptr(unsafe.Pointer(&n)))
	if err := rv("SCardTransmit", r); err != nil {
		return nil, err
	}
	if int(n) > len(recv) {
		n = uint32(len(recv))
	}
	return recv[:n], nil
}

func (d *dll) Disconnect(card uintptr) { syscall.SyscallN(d.disconnect, card, scardLeaveCard) }
