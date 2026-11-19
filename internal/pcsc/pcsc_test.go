package pcsc

import (
	"bytes"
	"errors"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakePcscd は pcsc-lite 1.9 の winscard_msg.h のとおりに答える偽の pcscd。
type fakePcscd struct {
	t         *testing.T
	ln        net.Listener
	sock      string
	readers   []string
	connectRV uint32
	major     uint32
	transmit  func(apdu []byte) []byte

	mu     sync.Mutex
	calls  []uint32
	apdus  [][]byte
	closed chan struct{}
}

func newFake(t *testing.T) *fakePcscd {
	t.Helper()
	f := &fakePcscd{t: t, sock: filepath.Join(t.TempDir(), "pcscd.comm"), major: protoMajor, closed: make(chan struct{}, 8)}
	ln, err := net.Listen("unix", f.sock)
	if err != nil {
		t.Skipf("unix socket を作れません: %v", err)
	}
	f.ln = ln
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakePcscd) serve(c net.Conn) {
	defer func() { c.Close(); f.closed <- struct{}{} }()
	for {
		h := make([]byte, 8)
		if _, err := io.ReadFull(c, h); err != nil {
			return
		}
		size, cmd := order.Uint32(h[0:]), order.Uint32(h[4:])
		body := make([]byte, size)
		if _, err := io.ReadFull(c, body); err != nil {
			return
		}
		f.mu.Lock()
		f.calls = append(f.calls, cmd)
		f.mu.Unlock()
		switch cmd {
		case cmdVersion:
			r := make([]byte, 12)
			order.PutUint32(r[0:], f.major)
			order.PutUint32(r[4:], protoMinor)
			c.Write(r)
		case cmdEstablishContext:
			r := make([]byte, 12)
			copy(r, body)
			order.PutUint32(r[4:], 0xBEEF)
			c.Write(r)
		case cmdGetReadersState:
			r := make([]byte, maxReaders*readerStateSize)
			for i, n := range f.readers {
				copy(r[i*readerStateSize:], n)
			}
			c.Write(r)
		case cmdConnect:
			r := append([]byte(nil), body...)
			base := 4 + maxReaderName
			order.PutUint32(r[base+8:], 7)        // hCard
			order.PutUint32(r[base+12:], protoT1) // dwActiveProtocol
			order.PutUint32(r[base+16:], f.connectRV)
			c.Write(r)
		case cmdTransmit:
			n := int(order.Uint32(body[12:]))
			apdu := make([]byte, n)
			if _, err := io.ReadFull(c, apdu); err != nil {
				return
			}
			f.mu.Lock()
			f.apdus = append(f.apdus, apdu)
			f.mu.Unlock()
			resp := f.transmit(apdu)
			r := append([]byte(nil), body...)
			order.PutUint32(r[24:], uint32(len(resp)))
			order.PutUint32(r[28:], rvOK)
			c.Write(append(r, resp...))
		case cmdDisconnect, cmdReleaseContext:
			c.Write(body)
		}
	}
}

func (f *fakePcscd) sawCommands() []uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uint32(nil), f.calls...)
}

func TestList_リーダーの名前とそのまま_内蔵かどうか(t *testing.T) {
	f := newFake(t)
	f.readers = []string{"PLEX PX-MLT5PE Smart Card Reader [ID: 0123] (0123) 00 00", "ACS ACR39U ICC Reader 00 00"}
	rs, err := New(f.sock).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 || rs[0].Name != f.readers[0] || !rs[0].Internal || rs[1].Name != f.readers[1] || rs[1].Internal {
		t.Errorf("%+v", rs)
	}
	// バージョン → コンテキストの順で、リーダーの一覧を取る
	got := f.sawCommands()
	if len(got) != 3 || got[0] != cmdVersion || got[1] != cmdEstablishContext || got[2] != cmdGetReadersState {
		t.Errorf("手順: %v", got)
	}
}

func TestList_リーダーが無ければ空(t *testing.T) {
	f := newFake(t)
	rs, err := New(f.sock).List()
	if err != nil || len(rs) != 0 {
		t.Errorf("%v %v", rs, err)
	}
}

func TestList_繋がらなければ理由(t *testing.T) {
	_, err := New(filepath.Join(t.TempDir(), "nothing.sock")).List()
	if err == nil || err.Error() == "" || !bytes.Contains([]byte(err.Error()), []byte("pcscd に繋がりません (")) {
		t.Errorf("%v", err)
	}
}

func TestList_プロトコルのバージョンが違えば断る(t *testing.T) {
	f := newFake(t)
	f.major = 3
	if _, err := New(f.sock).List(); err == nil || !bytes.Contains([]byte(err.Error()), []byte("バージョンが違います")) {
		t.Errorf("%v", err)
	}
}

func TestOpen_排他で開いてAPDUを送る(t *testing.T) {
	f := newFake(t)
	f.readers = []string{"R [ID: 1]"}
	f.transmit = func(apdu []byte) []byte { return append(append([]byte{0, 0, 0x21, 0x00}, apdu...), 0x90, 0x00) }
	l, err := New(f.sock).Open("R [ID: 1]")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := l.Transmit([]byte{0x90, 0x30, 0x00, 0x00, 0x00})
	if err != nil || !bytes.Equal(resp, []byte{0, 0, 0x21, 0, 0x90, 0x30, 0, 0, 0, 0x90, 0}) && !bytes.HasSuffix(resp, []byte{0x90, 0x00}) {
		t.Fatalf("%x %v", resp, err)
	}
	f.mu.Lock()
	if len(f.apdus) != 1 || !bytes.Equal(f.apdus[0], []byte{0x90, 0x30, 0, 0, 0}) {
		t.Errorf("サーバに届いた APDU: %x", f.apdus)
	}
	f.mu.Unlock()
	l.Close()
	select {
	case <-f.closed:
	case <-time.After(2 * time.Second):
		t.Error("閉じても接続が残っている")
	}
	cmds := f.sawCommands()
	want := []uint32{cmdVersion, cmdEstablishContext, cmdConnect, cmdTransmit, cmdDisconnect, cmdReleaseContext}
	if len(cmds) != len(want) {
		t.Fatalf("手順: %v", cmds)
	}
	for i := range want {
		if cmds[i] != want[i] {
			t.Errorf("手順: %v (want %v)", cmds, want)
			break
		}
	}
}

func TestOpen_カードが無ければ理由(t *testing.T) {
	f := newFake(t)
	f.connectRV = rvNoSmartcard
	_, err := New(f.sock).Open("R [ID: 1]")
	var pe *Error
	if !errors.As(err, &pe) || pe.RV != rvNoSmartcard || err.Error() != "pcscd のカードへの接続が失敗しました (SCARD_E_NO_SMARTCARD)" {
		t.Errorf("%v", err)
	}
}

func TestOpen_共有違反も理由に出す(t *testing.T) {
	f := newFake(t)
	f.connectRV = rvSharing
	_, err := New(f.sock).Open("R")
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("SCARD_E_SHARING_VIOLATION")) {
		t.Errorf("%v", err)
	}
}

func TestTransmit_閉じたあとと範囲の外は断る(t *testing.T) {
	f := newFake(t)
	f.transmit = func([]byte) []byte { return []byte{0x90, 0x00} }
	l, _ := New(f.sock).Open("R")
	if _, err := l.Transmit(nil); err == nil {
		t.Error("空の APDU は断る")
	}
	if _, err := l.Transmit(make([]byte, maxBuffer+1)); err == nil {
		t.Error("長すぎる APDU は断る")
	}
	l.Close()
	if _, err := l.Transmit([]byte{1}); err == nil {
		t.Error("閉じたあとは断る")
	}
}

func TestIsInternal(t *testing.T) {
	internal := []string{
		"PLEX PX-MLT5PE Smart Card Reader [ID: 1234] (1234) 00 00",
		"PIXELA PIX-DT295 Smart Card Reader [ID: 3657DC39D50EE6B5] (YD21153782) 01 00", // 実機の pcscd の名前
		"I-O DATA GV-MVP/XZ Smart Card Reader 00 00",                                   // [ID: …] が付かない機種
	}
	external := []string{
		"Generic Smart Card Reader Interface 00 00",
		"ACS ACR39U ICC Reader 00 00",
		"Gemalto PC Twin Reader 00 00",
	}
	for _, n := range internal {
		if !IsInternal(n) {
			t.Errorf("内蔵のはず: %q", n)
		}
	}
	for _, n := range external {
		if IsInternal(n) {
			t.Errorf("外付けのはず: %q", n)
		}
	}
}
