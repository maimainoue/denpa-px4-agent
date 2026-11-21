//go:build fakecard

package cardshare

import (
	"bytes"
	"testing"
	"time"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/b25"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/card"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/config"
)

// TestFakeCardShareRoundTrip は、試験カードを配る側にして、
// リモートから Init・ECM・契約確認・EMM が往復することと、
// TS から来た EMM が試験カードまで届くことを、端から端まで確かめます。
// 実機のカードは要りません。
func TestFakeCardShareRoundTrip(t *testing.T) {
	t.Setenv("FAKE_CARD", "1")
	fp := card.FakeProvider()
	giver := card.New([]card.Provider{fp}, nil, func(string, ...any) {}, card.Hooks{})
	nets, err := config.ParseShareNetworks(config.DefaultShareNetworks())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(0, nets, giver.LocalCard(), nil, 1, 0, func(string, ...any) {})
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	remote := NewRemoteCard(shareURL(srv), 1, func(string, ...any) {})

	init, err := remote.Init()
	if err != nil {
		t.Fatal(err)
	}
	if init.CASystemID != 0x0005 || len(init.IDs) != 1 || init.IDs[0] != (b25.CardID{0x00, 0x01, 0x23, 0x45, 0x67, 0x89}) {
		t.Fatalf("試験カードの init が違います: %+v", init)
	}

	answer, err := remote.Ecm([]byte{0x00, 0x01, 0x02, 0x03, 0x04, 0xaa, 0xbb, 0xcc})
	if err != nil {
		t.Fatal(err)
	}
	if answer.Code != 0x0200 || answer.Odd[0] != 0x11 || answer.Even[0] != 0x22 {
		t.Fatalf("試験カードの ECM が違います: %+v", answer)
	}

	code, err := remote.Confirm([]byte{0xe0, 0x00, 0x01, 0x02}, 0x1234)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0x0800 {
		t.Fatalf("試験カードの契約確認が違います: %#04x", code)
	}

	code, err = remote.Emm([]byte{0x36, 0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b})
	if err != nil {
		t.Fatal(err)
	}
	if code != 0x2100 {
		t.Fatalf("試験カードの EMM が違います: %#04x", code)
	}

	// TS から来た EMM が、貰う側の Stream で選ばれて、配る側の試験カードに届きます。
	taker := card.New(nil, nil, nil, card.Hooks{})
	taker.SetRemote(remote)
	s := taker.NewStream("test")
	s.SetDecrypt(true)
	mine := append([]byte{0x00, 0x01, 0x23, 0x45, 0x67, 0x89, 6}, bytes.Repeat([]byte{0xAA}, 6)...)
	other := append([]byte{9, 9, 9, 9, 9, 9, 6}, bytes.Repeat([]byte{0xBB}, 6)...)
	emm := append([]byte{0x84, 0xB0, byte(len(mine) + len(other) + 4)}, append(mine, other...)...)
	crc := fakeCRC32MPEG(emm)
	emm = append(emm, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
	cat := fakeSection(0x01, 0xFFFF, []byte{0x09, 0x04, 0x00, 0x05, 0xE0 | 0x01, 0x00}) // EMM PID 0x100
	before := fp.Calls(0x36)
	s.Process(append(fakePsiPacket(1, cat), fakePsiPacket(0x100, emm)...))
	deadline := time.Now().Add(5 * time.Second)
	for fp.Calls(0x36) == before {
		if time.Now().After(deadline) {
			t.Fatal("TS の EMM が試験カードに届きませんでした")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// 他のカード宛てのものは送りません。
	time.Sleep(200 * time.Millisecond)
	if fp.Calls(0x36) != before+1 {
		t.Fatalf("EMM を %d 回送っています", fp.Calls(0x36)-before)
	}
}

var fakeCRC32Table = func() [256]uint32 {
	var table [256]uint32
	for i := 0; i < 256; i++ {
		crc := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04C11DB7
			} else {
				crc <<= 1
			}
		}
		table[i] = crc
	}
	return table
}()

func fakeCRC32MPEG(b []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, x := range b {
		crc = crc<<8 ^ fakeCRC32Table[byte(crc>>24)^x]
	}
	return crc
}

// fakeSection は PSI のセクションを作ります (b25 の試験用と同じ形)。
func fakeSection(tableID byte, ext uint16, body []byte) []byte {
	n := 5 + len(body) + 4
	sec := []byte{tableID, 0xB0 | byte(n>>8), byte(n), byte(ext >> 8), byte(ext), 0xC1, 0, 0}
	sec = append(sec, body...)
	crc := fakeCRC32MPEG(sec)
	return append(sec, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

// fakePsiPacket は PSI のセクションを 1 パケットに入れます。
func fakePsiPacket(pid uint16, sec []byte) []byte {
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
