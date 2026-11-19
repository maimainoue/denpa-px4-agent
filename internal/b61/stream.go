package b61

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"
	"sync/atomic"
	"time"
)

const (
	caSystemACAS = 0x0005
	maxHold      = 16 << 20         // 鍵が届くまで溜める上限 (docs/b61.md の 7.2)
	maxHoldTime  = 10 * time.Second // これを超えたら、溜めたものを吐き出す
	ecmWait      = 4 * time.Second  // 別の型の鍵が要るとき、ECM の応答を待つ上限
	ecmRetry     = 30 * time.Second // ECM が失敗してから、次の ECM を試すまで
	cardRetry    = 10 * time.Second // カードを使えなかったとき、探し直すまで
)

// CardService は 4K の復号に要るカードの働き。internal/card の Manager が満たす。
type CardService interface {
	// B61Module は A-CAS のカードの CAS モジュール ID。A-CAS でなければエラー。
	B61Module() (uint64, error)
	// B61ECM は ECM から 32 バイトの鍵 (前半が奇数・後半が偶数) を貰う。
	B61ECM(ecm []byte) ([32]byte, error)
	// EMM は EMM を順番待ちに入れる (中で間引く)。
	EMM(payload []byte)
}

type keyType int

const (
	keyUnknown keyType = iota
	keyEven
	keyOdd
)

func (k keyType) other() keyType {
	if k == keyEven {
		return keyOdd
	}
	return keyEven
}

// program は PLT にある番組 1 つ。
type program struct {
	serviceID   uint16
	ecmPID      uint16 // 0xFFFF はスクランブルなし
	mptReceived bool
	assets      map[uint16]struct{} // スクランブルされた asset の packet id
}

// asset はスクランブルされた asset。
type asset struct {
	ecmPID  uint16
	keyType keyType // 最後に見たパケットの鍵の型
}

type ecmResult struct {
	done chan struct{}
	keys [32]byte
	err  error
}

// ecmState は ECM の packet id 1 つ。
type ecmState struct {
	version  int
	pending  *ecmResult
	received bool // 1 度は鍵を取れた
	failed   bool
	failedAt time.Time
	changed  keyType // 最後の ECM で変わった鍵の型
	odd      [16]byte
	even     [16]byte
}

type heldPacket struct{ off, size int }

// Stream は 1 つの選局の MMT/TLV を復号する。manager.Processor を満たす。
type Stream struct {
	cs   CardService
	logf func(format string, args ...any)

	decrypt atomic.Bool

	moduleID  uint64
	cardErr   string
	cardRetry time.Time

	tlv      []byte
	out      []byte
	programs map[uint16]*program
	si       map[uint16]*siBuf
	assets   map[uint16]*asset
	ecms     map[uint16]*ecmState

	initial bool // 最初の鍵が届くまで、出力を溜める
	held    []heldPacket
	started time.Time

	warnedIV bool
	Stats    struct{ Packets, Decrypted, Undecrypted atomic.Int64 }
}

// NewStream は選局 1 つぶんの復号を作る。
func NewStream(cs CardService, logf func(format string, args ...any)) *Stream {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	s := &Stream{cs: cs, logf: logf, initial: true}
	s.reset()
	return s
}

func (s *Stream) reset() {
	s.programs = map[uint16]*program{}
	s.assets = map[uint16]*asset{}
	s.ecms = map[uint16]*ecmState{}
	s.si = map[uint16]*siBuf{
		0x0000: {pid: 0x0000, kind: siPLT, version: -1},
		0x0001: {pid: 0x0001, kind: siCAT, version: -1},
	}
}

// SetDecrypt は鍵の要る利用者がいるか。いなければカードに問い合わせず、素通しにする。
func (s *Stream) SetDecrypt(on bool) { s.decrypt.Store(on) }

// Close は選局が終わったとき。復号した数と、できなかった数を残す。
func (s *Stream) Close() {
	ok, bad := s.Stats.Decrypted.Load(), s.Stats.Undecrypted.Load()
	if ok+bad > 0 {
		s.logf("4K の復号: 復号したパケット %d・復号できなかったパケット %d", ok, bad)
	}
}

// Process は塊を受けて、出せるぶんを返す。
func (s *Stream) Process(in []byte) []byte {
	if !s.decrypt.Load() || !s.ensureCard() {
		return s.passthrough(in)
	}
	if s.started.IsZero() {
		s.started = time.Now()
	}
	s.tlv = append(s.tlv, in...)
	s.feed()
	if s.initial && (len(s.out) > maxHold || time.Since(s.started) > maxHoldTime) {
		s.release("鍵が届かないまま、溜める上限に達しました")
	}
	if s.initial || len(s.out) == 0 {
		return nil
	}
	out := s.out
	s.out = nil
	return out
}

// Flush は溜めているぶんを返す (入力が終わったとき)。
func (s *Stream) Flush() []byte {
	s.release("")
	out := s.out
	s.out = nil
	return out
}

// passthrough は復号しないとき。溜めたものがあれば、先に出す。
func (s *Stream) passthrough(in []byte) []byte {
	if len(s.out) == 0 && len(s.tlv) == 0 {
		return in
	}
	out := append(s.out, in...)
	s.out, s.tlv, s.held = nil, nil, nil
	return out
}

// ensureCard はカードの CAS モジュール ID を持つ。使えなければ、理由を 1 回だけ出して、しばらく素通しにする。
func (s *Stream) ensureCard() bool {
	if s.moduleID != 0 {
		return true
	}
	if time.Now().Before(s.cardRetry) {
		return false
	}
	id, err := s.cs.B61Module()
	if err != nil {
		if s.cardErr != err.Error() {
			s.cardErr = err.Error()
			s.logf("4K の復号を使えません: %v", err)
		}
		s.cardRetry = time.Now().Add(cardRetry)
		return false
	}
	s.moduleID = id
	s.logf("4K の復号を始めます (CAS モジュール ID %012X)", id)
	return true
}

// feed は TLV を 1 つずつ切り出す。同期バイトの前の残りと、不正なパケットは捨てる。
func (s *Stream) feed() {
	pos := 0
	for len(s.tlv)-pos >= 4 {
		b := s.tlv[pos:]
		if b[0] != tlvSync {
			pos = nextSync(s.tlv, pos+1)
			continue
		}
		if !validTLVType(b[1]) {
			pos = nextSync(s.tlv, pos+1)
			continue
		}
		n := int(b[2])<<8 | int(b[3])
		if len(b) < 4+n {
			break
		}
		pkt := b[:4+n]
		switch pkt[1] {
		case tlvIPv6, tlvSI, tlvNull:
			s.out = append(s.out, pkt...)
		case tlvCompressed:
			off := mmtpOffset(pkt[4:])
			if off < 0 {
				break // 扱えないものは捨てる
			}
			start := len(s.out)
			s.out = append(s.out, pkt...)
			base := start + 4 + off
			s.mmtp(base, len(s.out)-base, false)
		}
		pos += 4 + n
	}
	s.tlv = append(s.tlv[:0], s.tlv[pos:]...)
}

func be16(b []byte) int { return int(b[0])<<8 | int(b[1]) }

// mmtp は出力の base から size バイトの MMTP パケットを読む。復号できるものは、その場で書き換える。
// decryptOnly は、溜めておいたものを後から復号するとき (信号は読まない)。
func (s *Stream) mmtp(base, size int, decryptOnly bool) {
	p := s.out[base : base+size]
	if len(p) < 12 {
		return
	}
	ptype := p[1] & 0x3F
	pid := uint16(be16(p[2:]))
	seq := uint32(p[8])<<24 | uint32(p[9])<<16 | uint32(p[10])<<8 | uint32(p[11])
	pay := 12
	if p[0]&2 != 0 {
		if len(p) < pay+4 {
			return
		}
		etype, elen := be16(p[pay:]), be16(p[pay+2:])
		start := pay + 4
		if len(p) < start+elen {
			return
		}
		pay = start + elen
		if etype == 0 {
			if s.headerExt(base, size, start, pay, pid, seq, decryptOnly) {
				return
			}
			p = s.out[base : base+size] // 溜めるときに out が変わることはないが、念のため取り直す
		}
	}
	if decryptOnly || ptype != 2 { // 信号
		return
	}
	if si := s.si[pid]; si != nil {
		s.onSI(si, p[pay:])
	}
}

// headerExt は拡張ヘッダーの並びを読んで、スクランブル制御 (種別 1) があれば復号する。
// パケットの読みを止めてよいとき (復号できずに断念したとき) は true。
func (s *Stream) headerExt(base, size, q, end int, pid uint16, seq uint32, decryptOnly bool) bool {
	p := s.out[base : base+size]
	for end-q >= 4 {
		htype := (int(p[q]&0x7F) << 8) | int(p[q+1])
		hlen := be16(p[q+2:])
		q += 4
		if end-q < hlen {
			return false
		}
		if htype == 1 && hlen > 0 && size-end >= 8 {
			if s.scramble(base, size, q, hlen, end, pid, seq, decryptOnly) {
				return true
			}
		}
		q += hlen
	}
	return false
}

// scramble はスクランブル制御 1 つ。q は値の先頭 (制御のバイト)。
func (s *Stream) scramble(base, size, q, hlen, end int, pid uint16, seq uint32, decryptOnly bool) bool {
	p := s.out[base : base+size]
	f := p[q]
	control := (f >> 3) & 3
	if control != 2 && control != 3 {
		return false
	}
	kt := keyEven
	if control == 3 {
		kt = keyOdd
	}
	hasID, hasMAC, hasIV := f&4 != 0, f&2 != 0, f&1 != 0
	ivOff := 1 // 制御のバイトの次から、識別子 (1)・MAC (2)・IV (16) の順
	if hasID {
		ivOff++
	}
	if hasMAC {
		ivOff += 2
	}
	if hasIV && hlen < ivOff+16 {
		hasIV = false
	}
	if hasIV && !s.warnedIV {
		s.warnedIV = true
		s.logf("IV を持つスクランブルのパケットです (この形は実機で確かめていません)")
	}
	a := s.assets[pid]
	if a == nil {
		if s.initial {
			s.hold(base, size)
		}
		return false
	}
	if !decryptOnly {
		a.keyType = kt
	}
	ecm := s.ecms[a.ecmPID]
	if ecm == nil || (ecm.pending == nil && !ecm.received) {
		if s.initial {
			s.hold(base, size)
		}
		return false
	}
	if ecm.failed {
		s.Stats.Undecrypted.Add(1)
		return true
	}
	if ecm.pending != nil && kt != ecm.changed {
		if !s.resolve(ecm, kt) {
			s.Stats.Undecrypted.Add(1)
			return true
		}
	}
	var key []byte
	if kt == keyEven {
		key = ecm.even[:]
	} else {
		key = ecm.odd[:]
	}
	var iv [16]byte
	if hasIV {
		copy(iv[:], p[q+ivOff:q+ivOff+16])
	} else {
		iv[0], iv[1] = byte(pid>>8), byte(pid)
		iv[2], iv[3], iv[4], iv[5] = byte(seq>>24), byte(seq>>16), byte(seq>>8), byte(seq)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return true
	}
	data := p[end+8:] // MPU ペイロードのヘッダー (8 バイト) のあと
	cipher.NewCTR(block, iv[:]).XORKeyStream(data, data)
	p[q] &^= 3 << 3 // スクランブルの印を消す
	s.Stats.Decrypted.Add(1)
	return false
}

func (s *Stream) hold(base, size int) { s.held = append(s.held, heldPacket{base, size}) }

// resolve は応答待ちの ECM の鍵を受け取る。待てなかった・失敗ならば false。
func (s *Stream) resolve(ecm *ecmState, kt keyType) bool {
	r := ecm.pending
	select {
	case <-r.done:
	case <-time.After(ecmWait):
		ecm.pending, ecm.failed, ecm.failedAt = nil, true, time.Now()
		s.logf("ECM の応答が %d 秒たっても来ません", int(ecmWait.Seconds()))
		return false
	}
	ecm.pending = nil
	if r.err != nil {
		ecm.failed, ecm.failedAt = true, time.Now()
		s.logf("ECM を使えません: %v", r.err)
		return false
	}
	var odd, even [16]byte
	copy(odd[:], r.keys[:16])
	copy(even[:], r.keys[16:])
	switch {
	case ecm.received && odd == ecm.odd:
		ecm.changed = keyEven
	case ecm.received && even == ecm.even:
		ecm.changed = keyOdd
	case ecm.changed == keyUnknown:
		ecm.changed = kt.other()
	}
	ecm.odd, ecm.even, ecm.received = odd, even, true
	return true
}

func (s *Stream) addECM(pid uint16) {
	if pid == 0 || pid == 0xFFFF {
		return
	}
	if _, ok := s.ecms[pid]; ok {
		return
	}
	s.ecms[pid] = &ecmState{version: -1}
	s.si[pid] = &siBuf{pid: pid, kind: siECM, version: -1}
}

// dropECM は、その ECM を使う番組がほかに無ければ手放す。
func (s *Stream) dropECM(pid uint16) {
	if pid == 0 || pid == 0xFFFF {
		return
	}
	for _, p := range s.programs {
		if p.ecmPID == pid {
			return
		}
	}
	delete(s.ecms, pid)
	delete(s.si, pid)
}

// onECM は ECM の表。本体の先頭 5 バイトのあとをカードに渡す。
func (s *Stream) onECM(si *siBuf, b []byte) {
	ecm := s.ecms[si.pid]
	if ecm == nil || len(b) < 5 {
		return
	}
	if ecm.failed && time.Since(ecm.failedAt) < ecmRetry {
		return
	}
	if b[2]&1 == 0 { // current_next
		return
	}
	version := int(b[2]>>1) & 0x1F
	if version == ecm.version {
		return
	}
	ecm.version = version
	ecm.failed = false
	data := append([]byte(nil), b[5:]...)
	r := &ecmResult{done: make(chan struct{})}
	ecm.pending = r
	go func() {
		r.keys, r.err = s.cs.B61ECM(data)
		close(r.done)
	}()
	if s.initial && s.readyToRelease() {
		s.release("")
	}
}

// onEMM は EMM の表。自分のカードの CAS モジュール ID のものだけ、カードに渡す。
func (s *Stream) onEMM(b []byte) {
	if s.moduleID == 0 || len(b) < 5 || b[2]&1 == 0 {
		return
	}
	b = b[5:]
	for len(b) >= 7+1+1 {
		var id uint64
		for _, x := range b[:6] {
			id = id<<8 | uint64(x)
		}
		n := int(b[6])
		if n < 2 || len(b) < 7+n {
			return
		}
		if id == s.moduleID {
			s.cs.EMM(append([]byte(nil), b[:7+n]...))
		}
		b = b[7+n:]
	}
}

// readyToRelease は、すべての番組の MPT が届いて、スクランブルされた番組の ECM がカードに渡ったか。
func (s *Stream) readyToRelease() bool {
	if len(s.programs) == 0 {
		return false
	}
	for _, p := range s.programs {
		if !p.mptReceived {
			return false
		}
		if p.ecmPID == 0xFFFF {
			continue
		}
		e := s.ecms[p.ecmPID]
		if e == nil || (e.pending == nil && !e.received) {
			return false
		}
	}
	return true
}

// checkNoScramble は、すべての MPT が届いて、スクランブルされた番組が 1 つも無いとき、溜めるのをやめる。
func (s *Stream) checkNoScramble() {
	if !s.initial || len(s.programs) == 0 {
		return
	}
	for _, p := range s.programs {
		if !p.mptReceived || p.ecmPID != 0xFFFF {
			return
		}
	}
	s.initial, s.held = false, nil
}

// release は溜めていたパケットを復号して、出力できるようにする。
func (s *Stream) release(why string) {
	if !s.initial {
		return
	}
	if why != "" {
		s.logf("%s", why)
	}
	s.initial = false
	held := s.held
	s.held = nil
	for _, h := range held {
		s.mmtp(h.off, h.size, true)
	}
}

// String はテストと診断のための表示。
func (s *Stream) String() string {
	return fmt.Sprintf("b61.Stream{programs=%d assets=%d ecms=%d held=%d}", len(s.programs), len(s.assets), len(s.ecms), len(s.held))
}
