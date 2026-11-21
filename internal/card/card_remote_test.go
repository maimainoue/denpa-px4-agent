package card

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/b25"
)

// fakeRemote は別の拠点から貰うカードのふりをする。
type fakeRemote struct {
	mu          sync.Mutex
	url         string
	init        b25.Info
	ecmRes      b25.ECMResult
	confirmCode uint16
	emmCode     uint16
	emmErr      error
	ecmCalls    int
	emmCalls    int
	emmPayloads [][]byte
}

func (f *fakeRemote) URL() string { return f.url }

func (f *fakeRemote) Init() (b25.Info, error)  { return f.init, nil }
func (f *fakeRemote) Check() (b25.Info, error) { return f.init, nil }

func (f *fakeRemote) Ecm(ecm []byte) (b25.ECMResult, error) {
	f.mu.Lock()
	f.ecmCalls++
	f.mu.Unlock()
	return f.ecmRes, nil
}

func (f *fakeRemote) Confirm(verification []byte, dateMJD uint16) (uint16, error) {
	return f.confirmCode, nil
}

func (f *fakeRemote) Emm(payload []byte) (uint16, error) {
	f.mu.Lock()
	f.emmCalls++
	f.emmPayloads = append(f.emmPayloads, append([]byte(nil), payload...))
	f.mu.Unlock()
	return f.emmCode, f.emmErr
}

func newFakeRemote() *fakeRemote {
	info := b25.Info{
		CASystemID: 0x0005,
		IDs:        []b25.CardID{{0x00, 0x01, 0x23, 0x45, 0x67, 0x89}},
	}
	for i := range info.SystemKey {
		info.SystemKey[i] = byte(0x10 + i)
	}
	for i := range info.CBCInit {
		info.CBCInit[i] = byte(0xa0 + i)
	}
	res := b25.ECMResult{Code: 0x0800}
	for i := range res.Odd {
		res.Odd[i] = 0x11
	}
	for i := range res.Even {
		res.Even[i] = 0x22
	}
	return &fakeRemote{
		url: "http://192.168.0.10:25261", init: info,
		ecmRes: res, confirmCode: 0x0800, emmCode: 0x2100,
	}
}

// TestManagerRemote は、別の拠点から貰う設定のとき、手元のカードではなく
// 貰う側を使うことを確かめます。
func TestManagerRemote(t *testing.T) {
	remote := newFakeRemote()
	m := New(nil, nil, nil, Hooks{})
	m.SetRemote(remote)

	info, err := m.Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.CASystemID != 0x0005 || len(info.IDs) == 0 {
		t.Fatalf("Info が違います: %+v", info)
	}

	ecm := []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0xaa, 0xbb, 0xcc}
	answer, err := m.ECM(ecm)
	if err != nil {
		t.Fatal(err)
	}
	if answer.Code != 0x0800 || answer.Odd[0] != 0x11 || answer.Even[0] != 0x22 {
		t.Fatalf("ECM の答えが違います: %+v", answer)
	}
	// 同じ ECM は控えているので、2 回目は送りません。
	if _, err := m.ECM(ecm); err != nil {
		t.Fatal(err)
	}
	remote.mu.Lock()
	ecmCalls := remote.ecmCalls
	remote.mu.Unlock()
	if ecmCalls != 1 {
		t.Fatalf("ECM を %d 回送っています", ecmCalls)
	}

	code, err := m.Confirm(context.Background(), []byte{0xe0, 0x00, 0x01, 0x02}, 0x1234)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0x0800 {
		t.Fatalf("契約確認の返り値が違います: %#04x", code)
	}

	// /denpa/card は remote を返し、readers は空です。
	st := m.Status(context.Background())
	if st["source"] != "remote" {
		t.Fatalf("source が remote ではありません: %v", st)
	}
	if st["remote"] != remote.url {
		t.Fatalf("remote が違います: %v", st)
	}
	ids, ok := st["ids"].([]string)
	if !ok || len(ids) != 1 || ids[0] != "0000004886718345" {
		t.Fatalf("ids が違います: %v", st["ids"])
	}
	readers, ok := st["readers"].([]map[string]any)
	if !ok || len(readers) != 0 {
		t.Fatalf("readers が空ではありません: %v", st["readers"])
	}
	if st["ok"] != true {
		t.Fatalf("ok が true ではありません: %v", st)
	}

	// 配る側は手元のカードを使います。手元のカードが無いので、ここでは失敗します。
	if _, err := m.LocalCard().Info(); err == nil {
		t.Fatal("LocalCard().Info() が成功しました。手元のカードは無いはずです")
	}
}

// TestManagerRemoteEmmUnsupported は、配る側が古くて EMM を受け取れないとき、
// EMM を送るのを止めることを確かめます。
func TestManagerRemoteEmmUnsupported(t *testing.T) {
	remote := newFakeRemote()
	remote.emmErr = ErrEmmUnsupported
	m := New(nil, nil, nil, Hooks{})
	m.SetRemote(remote)

	m.EMM([]byte{0x36, 0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a})
	// emmLoop が 1 件目を送るまで待ちます (1 秒に 1 件)。
	deadline := time.Now().Add(3 * time.Second)
	for {
		m.mu.Lock()
		unsupported := m.emmUnsupported
		m.mu.Unlock()
		if unsupported {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("EMM を送るのを止めませんでした")
		}
		time.Sleep(50 * time.Millisecond)
	}
	remote.mu.Lock()
	emmCalls := remote.emmCalls
	remote.mu.Unlock()
	if emmCalls != 1 {
		t.Fatalf("EMM を %d 回送っています", emmCalls)
	}

	// 止めたあとは、EMM を渡しても送りません。
	m.EMM([]byte{0x36, 0x00, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14})
	time.Sleep(100 * time.Millisecond)
	remote.mu.Lock()
	emmCalls = remote.emmCalls
	remote.mu.Unlock()
	if emmCalls != 1 {
		t.Fatalf("止めたあとに EMM を送っています (%d 回)", emmCalls)
	}
}

// ---- TS を作る道具 (EMM の端から端までの試験用) ----

// testCRC32Table は MPEG-2 の CRC の表です (b25 の psi.go と同じ式)。
var testCRC32Table = func() [256]uint32 {
	var t [256]uint32
	for i := 0; i < 256; i++ {
		c := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if c&0x80000000 != 0 {
				c = c<<1 ^ 0x04C11DB7
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return t
}()

func testCRC32MPEG(b []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, x := range b {
		crc = crc<<8 ^ testCRC32Table[byte(crc>>24)^x]
	}
	return crc
}

// testSection は PSI のセクションを作ります (b25 の試験用と同じ形)。
func testSection(tableID byte, ext uint16, body []byte) []byte {
	n := 5 + len(body) + 4
	sec := []byte{tableID, 0xB0 | byte(n>>8), byte(n), byte(ext >> 8), byte(ext), 0xC1, 0, 0}
	sec = append(sec, body...)
	crc := testCRC32MPEG(sec)
	return append(sec, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

// testPsiPacket は PSI のセクションを 1 パケットに入れます。
func testPsiPacket(pid uint16, sec []byte) []byte {
	p := make([]byte, 188)
	for i := range p {
		p[i] = 0xFF
	}
	p[0] = 0x47
	p[1] = 0x40 | byte(pid>>8)
	p[2] = byte(pid)
	p[3] = 0x10
	copy(p[4:], append([]byte{0}, sec...))
	return p
}

// TestManagerRemoteEmmFromStream は、TS から来た EMM が、配る側のカードの番号で
// 選ばれてリモートに送られることを、端から端まで確かめます。
// 手元のカードは無く、番号は配る側 (Init) のものです。
func TestManagerRemoteEmmFromStream(t *testing.T) {
	remote := newFakeRemote()
	m := New(nil, nil, nil, Hooks{})
	m.SetRemote(remote)
	s := m.NewStream("test")
	s.SetDecrypt(true)
	mine := append([]byte{0x00, 0x01, 0x23, 0x45, 0x67, 0x89, 6}, bytes.Repeat([]byte{0xAA}, 6)...)
	other := append([]byte{9, 9, 9, 9, 9, 9, 6}, bytes.Repeat([]byte{0xBB}, 6)...)
	emm := append([]byte{0x84, 0xB0, byte(len(mine) + len(other) + 4)}, append(mine, other...)...)
	crc := testCRC32MPEG(emm)
	emm = append(emm, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
	cat := testSection(0x01, 0xFFFF, []byte{0x09, 0x04, 0x00, 0x05, 0xE0 | 0x01, 0x00}) // EMM PID 0x100
	s.Process(append(testPsiPacket(1, cat), testPsiPacket(0x100, emm)...))
	// emmLoop が送るまで待ちます (1 秒に 1 件)。
	deadline := time.Now().Add(3 * time.Second)
	for {
		remote.mu.Lock()
		calls := remote.emmCalls
		remote.mu.Unlock()
		if calls == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("EMM がリモートに送られませんでした (%d 回)", calls)
		}
		time.Sleep(50 * time.Millisecond)
	}
	remote.mu.Lock()
	sent := remote.emmPayloads[0]
	remote.mu.Unlock()
	if !bytes.Equal(sent, mine) {
		t.Fatalf("送られた EMM が違います: %x", sent)
	}
	// 他のカード宛てのものは送りません。
	time.Sleep(200 * time.Millisecond)
	remote.mu.Lock()
	calls := remote.emmCalls
	remote.mu.Unlock()
	if calls != 1 {
		t.Fatalf("EMM を %d 回送っています", calls)
	}
}
