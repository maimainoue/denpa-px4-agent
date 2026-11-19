package card

import (
	"crypto/sha256"
	"errors"
	"fmt"
)

// 4K 放送 (ARIB STD-B61) の A-CAS カードへの問い合わせ (docs/b61.md の 8)。
// B-CAS 互換モード (card.go の acasP2) とは別のコマンドで、INT・ECM・EMM は P2 が 1 になる。

// b61Master は鍵の保護 (CKC) の応答から鍵を取り出すためのマスター鍵。
// 参考にした mmttmte/arib-b61-stream-test (Unlicense) と同じ値で、このカードで復号するために要る。
var b61Master = [32]byte{
	0x4F, 0x4C, 0x7C, 0xEB, 0x34, 0xFE, 0xB0, 0xA3, 0x1E, 0x41, 0x19, 0x51, 0xE1, 0x35, 0x15, 0x12,
	0x87, 0xD3, 0x3D, 0x33, 0xD4, 0x9B, 0x4F, 0x52, 0x05, 0x77, 0xF9, 0xEF, 0xE5, 0x56, 0x1F, 0x32,
}

const caSystemACAS = 0x0005

// B61Module は A-CAS のカードの CAS モジュール ID (INT の 9〜14 バイト目) を返す。
// 開いているカードが A-CAS でなければ、その旨のエラー。
func (m *Manager) B61Module() (uint64, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if err := m.ensureLocked(); err != nil {
		return 0, err
	}
	m.mu.Lock()
	acas, id := m.acas, m.b61Module
	m.mu.Unlock()
	if !acas {
		return 0, errors.New("4K の復号には A-CAS のカードが要ります (開いているのは B-CAS のカードです)")
	}
	if id != 0 {
		return id, nil
	}
	resp, err := m.transmit([]byte{0x90, 0x30, 0x00, 0x01, 0x00}, 19)
	if err != nil {
		return 0, err
	}
	id, err = ParseB61INT(resp)
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	m.b61Module = id
	m.mu.Unlock()
	return id, nil
}

// ParseB61INT は INT (4K) の応答から CAS モジュール ID を取る。
func ParseB61INT(resp []byte) (uint64, error) {
	if len(resp) < 19 {
		return 0, fmt.Errorf("INT (4K) の応答が短すぎます (%d バイト)", len(resp))
	}
	if sw(resp) != 0x9000 {
		return 0, fmt.Errorf("INT (4K) のステータスが 0x%04X です", sw(resp))
	}
	if c := code(resp); c != 0x2100 {
		return 0, fmt.Errorf("INT (4K) の返り値が 0x%04X です", c)
	}
	if id := uint16(resp[6])<<8 | uint16(resp[7]); id != caSystemACAS {
		return 0, fmt.Errorf("A-CAS ではありません (CA_system_id 0x%04X)", id)
	}
	var v uint64
	for _, b := range resp[8:14] {
		v = v<<8 | uint64(b)
	}
	return v, nil
}

func sw(resp []byte) uint16 {
	if len(resp) < 2 {
		return 0
	}
	return uint16(resp[len(resp)-2])<<8 | uint16(resp[len(resp)-1])
}

// B61ECM は ECM をカードに渡して、32 バイトの鍵 (前半が奇数・後半が偶数) を返す。
// CKC と ECM は、間に他のコマンドを挟まずに続けて送る (挟むと鍵が化ける) ので、順番待ちを持ったまま 2 つ送る。
func (m *Manager) B61ECM(ecm []byte) ([32]byte, error) {
	var keys [32]byte
	if len(ecm) < 27 || len(ecm) > 255 {
		return keys, fmt.Errorf("ECM の長さは 27〜255 バイトです (%d)", len(ecm))
	}
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if err := m.ensureLocked(); err != nil {
		return keys, err
	}
	m.mu.Lock()
	acas := m.acas
	m.mu.Unlock()
	if !acas {
		return keys, errors.New("4K の復号には A-CAS のカードが要ります")
	}
	var a0init [8]byte
	ckc := append([]byte{0x90, 0xA0, 0x00, 0x01, 0x10, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x8A, 0xF7}, a0init[:]...)
	ckc = append(ckc, 0x00)
	r, err := m.transmit(ckc, 6)
	if err != nil {
		return keys, err
	}
	kcl, err := deriveKCL(r, a0init)
	if err != nil {
		return keys, err
	}
	req := append([]byte{0x90, 0x34, 0x00, 0x01, byte(len(ecm))}, ecm...)
	req = append(req, 0x00)
	resp, err := m.transmit(req, 6)
	if err != nil {
		return keys, err
	}
	return unwrapKeys(resp, kcl, ecm)
}

// deriveKCL は CKC の応答 (ちょうど 48 バイト・ステータス 9000・返り値 2100) から、鍵の保護の鍵を作る。
func deriveKCL(resp []byte, a0init [8]byte) ([32]byte, error) {
	var kcl [32]byte
	if len(resp) != 48 {
		return kcl, fmt.Errorf("CKC の応答の長さが違います (%d バイト)", len(resp))
	}
	if s := sw(resp); s != 0x9000 {
		return kcl, fmt.Errorf("CKC のステータスが 0x%04X です", s)
	}
	if c := code(resp); c != 0x2100 {
		return kcl, fmt.Errorf("CKC の返り値が 0x%04X です", c)
	}
	h := sha256.New()
	h.Write(b61Master[:])
	h.Write(a0init[:])
	h.Write(resp[6:14])
	copy(kcl[:], h.Sum(nil))
	return kcl, nil
}

// unwrapKeys は ECM の応答から鍵を取り出す。返り値が 0800・0600・4680 のときだけ鍵がある。
func unwrapKeys(resp []byte, kcl [32]byte, ecm []byte) ([32]byte, error) {
	var keys [32]byte
	if len(resp) < 6+32+2 {
		return keys, fmt.Errorf("ECM の応答が短すぎます (%d バイト)", len(resp))
	}
	if s := sw(resp); s != 0x9000 {
		return keys, fmt.Errorf("ECM のステータスが 0x%04X です", s)
	}
	switch c := code(resp); c {
	case 0x0800, 0x0600, 0x4680:
	case 0xA103, 0x8901, 0x8701, 0x8902, 0x8702, 0x8903, 0x8703, 0x8700, 0x8608:
		return keys, fmt.Errorf("契約がありません (ECM の返り値 0x%04X)", c)
	default:
		return keys, fmt.Errorf("カードが鍵を返しません (ECM の返り値 0x%04X)", c)
	}
	h := sha256.New()
	h.Write(kcl[:])
	h.Write(ecm[4:27])
	sum := h.Sum(nil)
	for i := range keys {
		keys[i] = sum[i] ^ resp[6+i]
	}
	return keys, nil
}
