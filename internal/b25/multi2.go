// Package b25 は ARIB STD-B25 のスクランブル解除 (仕様書 9 章)。
// MULTI2 と、PAT・PMT・CAT・ECM・EMM を読んで ES を復号する TS の処理を持つ。
// カードへの問い合わせは CardService の向こう側に置く。
package b25

import "encoding/binary"

// MULTI2 のラウンド数 (ARIB STD-B25)。
const multi2Round = 4

type core struct{ l, r uint32 }

func rol(x uint32, n uint) uint32 { return x<<n | x>>(32-n) }

func pi1(s core) core { return core{s.l, s.r ^ s.l} }

func pi2(s core, k1 uint32) core {
	t0 := s.r + k1
	t1 := rol(t0, 1) + t0 - 1
	t2 := rol(t1, 4) ^ t1
	return core{s.l ^ t2, s.r}
}

func pi3(s core, k2, k3 uint32) core {
	t0 := s.l + k2
	t1 := rol(t0, 2) + t0 + 1
	t2 := rol(t1, 8) ^ t1
	t3 := t2 + k3
	t4 := rol(t3, 1) - t3
	t5 := rol(t4, 16) ^ (t4 | s.l)
	return core{s.l, s.r ^ t5}
}

func pi4(s core, k4 uint32) core {
	t0 := s.r + k4
	t1 := rol(t0, 2) + t0 + 1
	return core{s.l ^ t1, s.r}
}

// Multi2 は system key と CBC の初期値を持ち、スクランブル鍵 1 つぶんで復号する。
type Multi2 struct {
	sys  [8]uint32
	cbc  core
	work [8]uint32
}

// NewMulti2 は system key (32 バイト) と CBC の初期値 (8 バイト) から作る。
func NewMulti2(sysKey [32]byte, cbcInit [8]byte) *Multi2 {
	m := &Multi2{}
	for i := range m.sys {
		m.sys[i] = binary.BigEndian.Uint32(sysKey[4*i:])
	}
	m.cbc = core{binary.BigEndian.Uint32(cbcInit[0:]), binary.BigEndian.Uint32(cbcInit[4:])}
	return m
}

// SetScrambleKey はスクランブル鍵 (8 バイト) から作業鍵を作る。
func (m *Multi2) SetScrambleKey(key [8]byte) {
	s := m.sys
	b := core{binary.BigEndian.Uint32(key[0:]), binary.BigEndian.Uint32(key[4:])}
	b = pi1(b)
	b = pi2(b, s[0])
	m.work[0] = b.l
	b = pi3(b, s[1], s[2])
	m.work[1] = b.r
	b = pi4(b, s[3])
	m.work[2] = b.l
	b = pi1(b)
	m.work[3] = b.r
	b = pi2(b, s[4])
	m.work[4] = b.l
	b = pi3(b, s[5], s[6])
	m.work[5] = b.r
	b = pi4(b, s[7])
	m.work[6] = b.l
	b = pi1(b)
	m.work[7] = b.r
}

func (m *Multi2) encryptBlock(s core) core {
	w := &m.work
	for i := 0; i < multi2Round; i++ {
		s = pi1(s)
		s = pi2(s, w[0])
		s = pi3(s, w[1], w[2])
		s = pi4(s, w[3])
		s = pi1(s)
		s = pi2(s, w[4])
		s = pi3(s, w[5], w[6])
		s = pi4(s, w[7])
	}
	return s
}

func (m *Multi2) decryptBlock(s core) core {
	w := &m.work
	for i := 0; i < multi2Round; i++ {
		s = pi4(s, w[7])
		s = pi3(s, w[5], w[6])
		s = pi2(s, w[4])
		s = pi1(s)
		s = pi4(s, w[3])
		s = pi3(s, w[1], w[2])
		s = pi2(s, w[0])
		s = pi1(s)
	}
	return s
}

// Decrypt は TS パケット 1 つぶんのペイロードを、その場で復号する。
// 8 バイトごとに CBC、端数は CBC の状態を暗号化した値との OFB (STD-B25、libaribb25 と同じ)。
func (m *Multi2) Decrypt(data []byte) {
	cbc := m.cbc
	n := len(data) / 8
	for i := 0; i < n; i++ {
		p := data[8*i:]
		src := core{binary.BigEndian.Uint32(p[0:]), binary.BigEndian.Uint32(p[4:])}
		d := m.decryptBlock(src)
		d.l ^= cbc.l
		d.r ^= cbc.r
		binary.BigEndian.PutUint32(p[0:], d.l)
		binary.BigEndian.PutUint32(p[4:], d.r)
		cbc = src
	}
	if rest := len(data) % 8; rest > 0 {
		e := m.encryptBlock(cbc)
		var ks [8]byte
		binary.BigEndian.PutUint32(ks[0:], e.l)
		binary.BigEndian.PutUint32(ks[4:], e.r)
		tail := data[8*n:]
		for i := range tail {
			tail[i] ^= ks[i]
		}
	}
}

// Encrypt は Decrypt の逆 (試験用。放送には使わない)。
func (m *Multi2) Encrypt(data []byte) {
	cbc := m.cbc
	n := len(data) / 8
	for i := 0; i < n; i++ {
		p := data[8*i:]
		s := core{binary.BigEndian.Uint32(p[0:]) ^ cbc.l, binary.BigEndian.Uint32(p[4:]) ^ cbc.r}
		c := m.encryptBlock(s)
		binary.BigEndian.PutUint32(p[0:], c.l)
		binary.BigEndian.PutUint32(p[4:], c.r)
		cbc = c
	}
	if rest := len(data) % 8; rest > 0 {
		e := m.encryptBlock(cbc)
		var ks [8]byte
		binary.BigEndian.PutUint32(ks[0:], e.l)
		binary.BigEndian.PutUint32(ks[4:], e.r)
		tail := data[8*n:]
		for i := range tail {
			tail[i] ^= ks[i]
		}
	}
}
