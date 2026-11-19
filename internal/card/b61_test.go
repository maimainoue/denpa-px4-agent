package card

import (
	"bytes"
	"crypto/sha256"
	"sync"
	"testing"
)

// b61Link は 4K に対応した A-CAS カードのふり。B-CAS の INT (P2=0) は断り、互換モードの INT (P2=2) と 4K の INT (P2=1) に答える。
type b61Link struct {
	mu     sync.Mutex
	sent   [][]byte
	keys   [32]byte
	ecmRC  uint16
	r      [8]byte
	module [6]byte
}

func (l *b61Link) Close() error { return nil }

func (l *b61Link) Transmit(a []byte) ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sent = append(l.sent, append([]byte(nil), a...))
	sw := []byte{0x90, 0x00}
	switch {
	case a[1] == 0x30 && a[3] == 0:
		return []byte{0x6A, 0x86}, nil
	case a[1] == 0x30 && a[3] == 2: // 互換モード
		r := resp(0x2100, 57)
		r[6], r[7] = 0x00, 0x05
		return r, nil
	case a[1] == 0x30 && a[3] == 1: // 4K
		r := []byte{0x00, 0x11, 0x00, 0x00, 0x21, 0x00, 0x00, 0x05}
		r = append(r, l.module[:]...)
		r = append(r, 0x02, 0x50, 0x01, 0x08, 0x01)
		return append(r, sw...), nil
	case a[1] == 0x32:
		return resp(0x2100, 7), nil
	case a[1] == 0xA0 && a[3] == 1:
		r := []byte{0x00, 0x00, 0x00, 0x00, 0x21, 0x00}
		r = append(r, l.r[:]...)
		r = append(r, make([]byte, 32)...)
		return append(r, sw...), nil
	case a[1] == 0x34 && a[3] == 1:
		ecm := a[5 : 5+int(a[4])]
		var a0init [8]byte
		kcl := sha256.Sum256(append(append(append([]byte{}, b61Master[:]...), a0init[:]...), l.r[:]...))
		h := sha256.Sum256(append(append([]byte{}, kcl[:]...), ecm[4:27]...))
		r := []byte{0x00, 0x00, 0x00, 0x00, byte(l.ecmRC >> 8), byte(l.ecmRC)}
		for i := 0; i < 32; i++ {
			r = append(r, h[i]^l.keys[i])
		}
		r = append(r, 0x00, 0x00)
		return append(r, sw...), nil
	}
	return []byte{0x6D, 0x00}, nil
}

func newB61Manager(l *b61Link) *Manager {
	return New([]Provider{b61Provider{l}}, nil, nil, Hooks{})
}

type b61Provider struct{ l *b61Link }

func (p b61Provider) List() ([]ReaderInfo, error) { return []ReaderInfo{{Name: "X"}}, nil }
func (p b61Provider) Open(string) (Link, error)   { return p.l, nil }

func TestB61Module_4Kの初期化でモジュールIDを読む(t *testing.T) {
	l := &b61Link{module: [6]byte{0x09, 0x21, 0x9E, 0xA4, 0x63, 0x27}}
	m := newB61Manager(l)
	id, err := m.B61Module()
	if err != nil || id != 0x09219EA46327 {
		t.Fatalf("%x %v", id, err)
	}
	n := len(l.sent)
	if id2, _ := m.B61Module(); id2 != id || len(l.sent) != n {
		t.Errorf("2 回目はカードに聞かない (%d → %d)", n, len(l.sent))
	}
}

func TestB61Module_B_CASのカードでは断る(t *testing.T) {
	m := New([]Provider{&fakeProvider{readers: []ReaderInfo{{Name: "A", Internal: true}}, links: map[string]*fakeLink{"A": {}}}}, nil, nil, Hooks{})
	if _, err := m.B61Module(); err == nil {
		t.Error("B-CAS のカードで通ってしまった")
	}
	if _, err := m.B61ECM(make([]byte, 30)); err == nil {
		t.Error("B-CAS のカードで ECM が通ってしまった")
	}
}

func TestB61ECM_鍵を取り出す_CKCとECMは続けて送る(t *testing.T) {
	var keys [32]byte
	for i := range keys {
		keys[i] = byte(0xC0 + i)
	}
	l := &b61Link{keys: keys, ecmRC: 0x0800, r: [8]byte{1, 2, 3, 4, 5, 6, 7, 8}}
	m := newB61Manager(l)
	ecm := bytes.Repeat([]byte{0x5A}, 40)
	got, err := m.B61ECM(ecm)
	if err != nil || got != keys {
		t.Fatalf("%x %v", got, err)
	}
	// 送った順: B-CAS の INT・互換の INT・IDI のあとに、CKC → ECM が間を空けずに続く
	var ins []byte
	for _, a := range l.sent {
		ins = append(ins, a[1])
	}
	if !bytes.HasSuffix(ins, []byte{0xA0, 0x34}) {
		t.Errorf("CKC → ECM の順に続けて送るはず: % x", ins)
	}
	// CKC の本文
	for _, a := range l.sent {
		if a[1] == 0xA0 {
			want := []byte{0x90, 0xA0, 0x00, 0x01, 0x10, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x8A, 0xF7, 0, 0, 0, 0, 0, 0, 0, 0, 0x00}
			if !bytes.Equal(a, want) {
				t.Errorf("CKC: % x", a)
			}
		}
	}
}

func TestB61ECM_契約が無ければ理由つきで断る(t *testing.T) {
	l := &b61Link{ecmRC: 0xA103}
	m := newB61Manager(l)
	if _, err := m.B61ECM(make([]byte, 30)); err == nil || !bytes.Contains([]byte(err.Error()), []byte("契約がありません")) {
		t.Errorf("%v", err)
	}
	l.ecmRC = 0x1234
	if _, err := m.B61ECM(make([]byte, 30)); err == nil || !bytes.Contains([]byte(err.Error()), []byte("0x1234")) {
		t.Errorf("%v", err)
	}
	if _, err := m.B61ECM(make([]byte, 10)); err == nil {
		t.Error("短い ECM が通った")
	}
}

func TestParseB61INT(t *testing.T) {
	ok := []byte{0x00, 0x11, 0x00, 0x00, 0x21, 0x00, 0x00, 0x05, 0x09, 0x21, 0x9E, 0xA4, 0x63, 0x27, 0x02, 0x50, 0x01, 0x08, 0x01, 0x90, 0x00}
	if id, err := ParseB61INT(ok); err != nil || id != 0x09219EA46327 {
		t.Errorf("%x %v", id, err)
	}
	bad := append([]byte(nil), ok...)
	bad[7] = 0x01 // B-CAS (CA_system_id が違う)
	for name, b := range map[string][]byte{"短い": ok[:10], "ステータス": append(append([]byte(nil), ok[:19]...), 0x6A, 0x86), "返り値": func() []byte { x := append([]byte(nil), ok...); x[4] = 0x12; return x }(), "CA": bad} {
		if _, err := ParseB61INT(b); err == nil {
			t.Errorf("%s: 通ってしまった", name)
		}
	}
}

func TestDeriveKCL_応答の長さと返り値を確かめる(t *testing.T) {
	var a0 [8]byte
	good := append([]byte{0, 0, 0, 0, 0x21, 0x00, 1, 2, 3, 4, 5, 6, 7, 8}, make([]byte, 32)...)
	good = append(good, 0x90, 0x00)
	if _, err := deriveKCL(good, a0); err != nil {
		t.Fatal(err)
	}
	if _, err := deriveKCL(good[:47], a0); err == nil {
		t.Error("短い応答が通った")
	}
	bad := append([]byte(nil), good...)
	bad[47] = 0x01
	if _, err := deriveKCL(bad, a0); err == nil {
		t.Error("ステータスが違うのに通った")
	}
}
