//go:build darwin

package winscard

import (
	"errors"
	"fmt"
	"unsafe"

	"github.com/ebitengine/purego"
)

// DefaultDLL は macOS の PCSC.framework。Big Sur 以降はファイルが dyld の共有キャッシュの中にあり、
// ディスクの上には無くても dlopen では開ける。
func DefaultDLL() string { return "/System/Library/Frameworks/PCSC.framework/PCSC" }

const (
	scardScopeSystem = 2
	scardShareExcl   = 1
	scardProtoT1     = 2
	scardLeaveCard   = 0
	maxRecv          = 1024 // 短い APDU の応答 (最大 256 + SW) に十分
)

// framework は読んだ PCSC.framework。cgo を使わず、purego で dlopen・dlsym して呼ぶ
// (cgo なしで他の OS からクロスビルドできるようにするため)。
//
// macOS の PCSC.framework は LONG が int32、DWORD が uint32、SCARDCONTEXT・SCARDHANDLE が int32
// (pcsc-lite の Linux のように long ではない)。リーダーの名前は UTF-8 (W の関数は無い)。
type framework struct {
	establish   func(scope uint32, r1, r2 unsafe.Pointer, ctx *int32) int32
	release     func(ctx int32) int32
	listReaders func(ctx int32, groups, readers unsafe.Pointer, n *uint32) int32
	connect     func(ctx int32, reader unsafe.Pointer, share, protos uint32, card *int32, active *uint32) int32
	transmit    func(card int32, sendPci, send unsafe.Pointer, sendLen uint32, recvPci, recv unsafe.Pointer, recvLen *uint32) int32
	disconnect  func(card int32, disposition uint32) int32
}

// LoadDLL は PCSC.framework (か、同じ API の共有ライブラリ) を読む。
func LoadDLL(path string) (API, error) {
	h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, err
	}
	f := &framework{}
	for _, e := range []struct {
		fn   any
		name string
	}{
		{&f.establish, "SCardEstablishContext"},
		{&f.release, "SCardReleaseContext"},
		{&f.listReaders, "SCardListReaders"},
		{&f.connect, "SCardConnect"},
		{&f.transmit, "SCardTransmit"},
		{&f.disconnect, "SCardDisconnect"},
	} {
		if err := register(e.fn, h, e.name); err != nil {
			return nil, err
		}
	}
	return f, nil
}

// register は関数が無いときの panic をエラーにする。
func register(fn any, h uintptr, name string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%s がありません: %v", name, r)
		}
	}()
	purego.RegisterLibFunc(fn, h, name)
	return nil
}

func rv(op string, r int32) error {
	if uint32(r) == RVOK {
		return nil
	}
	return &Error{Op: op, RV: uint32(r)}
}

func (f *framework) EstablishContext() (uintptr, error) {
	var ctx int32
	if err := rv("SCardEstablishContext", f.establish(scardScopeSystem, nil, nil, &ctx)); err != nil {
		return 0, err
	}
	return uintptr(uint32(ctx)), nil
}

func (f *framework) ReleaseContext(ctx uintptr) { f.release(int32(uint32(ctx))) }

func (f *framework) ListReaders(ctx uintptr) ([]string, error) {
	c := int32(uint32(ctx))
	var n uint32
	r := f.listReaders(c, nil, nil, &n)
	if uint32(r) == RVNoReaders {
		return nil, nil
	}
	if err := rv("SCardListReaders", r); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	buf := make([]byte, n)
	r = f.listReaders(c, nil, unsafe.Pointer(&buf[0]), &n)
	if uint32(r) == RVNoReaders {
		return nil, nil
	}
	if err := rv("SCardListReaders", r); err != nil {
		return nil, err
	}
	if int(n) > len(buf) {
		n = uint32(len(buf))
	}
	return splitMultiUTF8(buf[:n]), nil
}

func (f *framework) Connect(ctx uintptr, reader string) (uintptr, uint32, error) {
	name := append([]byte(reader), 0)
	for _, b := range name[:len(name)-1] {
		if b == 0 {
			return 0, 0, errors.New("リーダーの名前に NUL があります")
		}
	}
	var h int32
	var proto uint32
	r := f.connect(int32(uint32(ctx)), unsafe.Pointer(&name[0]), scardShareExcl, scardProtoT1, &h, &proto)
	if err := rv("SCardConnect", r); err != nil {
		return 0, 0, err
	}
	return uintptr(uint32(h)), proto, nil
}

// scardIORequest は SCARD_IO_REQUEST (dwProtocol と cbPciLength が DWORD)。
type scardIORequest struct {
	Protocol  uint32
	PciLength uint32
}

func (f *framework) Transmit(card uintptr, proto uint32, apdu []byte) ([]byte, error) {
	if len(apdu) == 0 {
		return nil, errors.New("APDU が空です")
	}
	pci := scardIORequest{Protocol: proto, PciLength: uint32(unsafe.Sizeof(scardIORequest{}))}
	recv := make([]byte, maxRecv)
	n := uint32(len(recv))
	r := f.transmit(int32(uint32(card)), unsafe.Pointer(&pci), unsafe.Pointer(&apdu[0]), uint32(len(apdu)),
		nil, unsafe.Pointer(&recv[0]), &n)
	if err := rv("SCardTransmit", r); err != nil {
		return nil, err
	}
	if int(n) > len(recv) {
		n = uint32(len(recv))
	}
	return recv[:n], nil
}

func (f *framework) Disconnect(card uintptr) {
	f.disconnect(int32(uint32(card)), scardLeaveCard)
}
