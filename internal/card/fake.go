//go:build fakecard

// 試験カード (FAKE_CARD)。-tags fakecard で組んだときだけ入り、FAKE_CARD=1 のときだけ使います。
// denpa の BCasTests と同じ固定の値 (INT・IDI・ECM の応答) を返します。実機のカードは要りません。
package card

import (
	"encoding/binary"
	"fmt"
	"os"
	"sync"
)

// FakeCardEnabled は試験カードを使うかです。FAKE_CARD=1 のときだけ true です。
// タグだけでは有効になりません。
func FakeCardEnabled() bool { return os.Getenv("FAKE_CARD") == "1" }

// 試験カードの固定の値 (こちら側の実装の FakeCard と同じ)。
var (
	fakeSystemKey = [32]byte{0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20, 0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28, 0x29, 0x2a, 0x2b, 0x2c, 0x2d, 0x2e, 0x2f}
	fakeCBCInit   = [8]byte{0xa0, 0xa1, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7}
	fakeCardID    = [6]byte{0x00, 0x01, 0x23, 0x45, 0x67, 0x89}
)

const (
	fakeCASystemID = 0x0005
	fakeECMCode    = 0x0200
	fakeConfirm    = 0x0800
	fakeEMMAccept  = 0x2100
)

// FakeProvider は試験カードの Provider です。FakeCardEnabled が false のときはリーダーを出しません。
func FakeProvider() *FakeCardProvider { return &FakeCardProvider{} }

// FakeCardProvider は試験カードの Provider (リーダー名 "fake")。
type FakeCardProvider struct {
	mu   sync.Mutex
	link *fakeCardLink
}

// List は試験カードのリーダーを並べます。FAKE_CARD=1 のときだけです。
func (p *FakeCardProvider) List() ([]ReaderInfo, error) {
	if !FakeCardEnabled() {
		return nil, nil
	}
	return []ReaderInfo{{Name: "fake", Internal: true}}, nil
}

// IsFakeCard は試験カードの Provider であることを表します。
// Manager が、登録の順に関わらず試験カードを先に試すために使います。
func (p *FakeCardProvider) IsFakeCard() bool { return FakeCardEnabled() }

// Open は試験カードを開きます。名前は "fake" だけです。
func (p *FakeCardProvider) Open(name string) (Link, error) {
	if !FakeCardEnabled() || name != "fake" {
		return nil, fmt.Errorf("試験カードはありません (FAKE_CARD=1)")
	}
	l := &fakeCardLink{}
	p.mu.Lock()
	p.link = l
	p.mu.Unlock()
	return l, nil
}

// Calls は開いた試験カードに送られたコマンドの回数です。テスト用です。まだ開いていなければ 0 です。
func (p *FakeCardProvider) Calls(ins byte) int {
	p.mu.Lock()
	l := p.link
	p.mu.Unlock()
	if l == nil {
		return 0
	}
	return l.calls(ins)
}

// fakeCardLink は試験カードの Link です。APDU に固定の答えを返します。
type fakeCardLink struct {
	mu     sync.Mutex
	counts map[byte]int
}

func (l *fakeCardLink) calls(ins byte) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.counts[ins]
}

func (l *fakeCardLink) count(ins byte) {
	l.mu.Lock()
	if l.counts == nil {
		l.counts = map[byte]int{}
	}
	l.counts[ins]++
	l.mu.Unlock()
}

// Close は何もしません。
func (l *fakeCardLink) Close() error { return nil }

// Transmit は APDU を受けて、固定の応答を返します。応答は 4 バイトの頭 + 返り値 2 バイト + 中身です。
func (l *fakeCardLink) Transmit(apdu []byte) ([]byte, error) {
	if len(apdu) < 5 {
		return nil, fmt.Errorf("APDU が短すぎます (%d バイト)", len(apdu))
	}
	ins := apdu[1]
	l.count(ins)
	var body []byte
	if len(apdu) > 5 { // Lc と中身と Le があるときだけ
		lc := int(apdu[4])
		if 5+lc > len(apdu) {
			return nil, fmt.Errorf("APDU の長さがおかしいです (%d バイト)", len(apdu))
		}
		body = apdu[5 : 5+lc]
	}
	switch ins {
	case 0x30: // INT
		resp := make([]byte, 57)
		binary.BigEndian.PutUint16(resp[4:6], fakeEMMAccept)
		binary.BigEndian.PutUint16(resp[6:8], fakeCASystemID)
		copy(resp[16:48], fakeSystemKey[:])
		copy(resp[48:56], fakeCBCInit[:])
		return resp, nil
	case 0x32: // IDI
		resp := make([]byte, 17)
		binary.BigEndian.PutUint16(resp[4:6], fakeEMMAccept)
		resp[6] = 1
		copy(resp[9:15], fakeCardID[:])
		return resp, nil
	case 0x34: // ECM
		if len(body) < 1 || len(body) > 255 {
			return nil, fmt.Errorf("ECM の長さがおかしいです (%d バイト)", len(body))
		}
		resp := make([]byte, 25)
		binary.BigEndian.PutUint16(resp[4:6], fakeECMCode)
		for i := 0; i < 8; i++ {
			resp[6+i] = 0x11
			resp[14+i] = 0x22
		}
		return resp, nil
	case 0x3C: // 契約確認
		if len(body) < 1 {
			return nil, fmt.Errorf("契約確認データがありません")
		}
		resp := make([]byte, 6)
		binary.BigEndian.PutUint16(resp[4:6], fakeConfirm)
		return resp, nil
	case 0x36: // EMM
		if len(body) < 13 || len(body) > 255 {
			return nil, fmt.Errorf("EMM の長さがおかしいです (%d バイト)", len(body))
		}
		resp := make([]byte, 6)
		binary.BigEndian.PutUint16(resp[4:6], fakeEMMAccept)
		return resp, nil
	}
	return []byte{0x6A, 0x86}, nil // 分からないコマンドは断る
}
