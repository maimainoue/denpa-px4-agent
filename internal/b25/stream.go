package b25

import (
	"bytes"
	"sync/atomic"
	"time"
)

// CardID はカードの番号 (個別とグループ) の 6 バイト。
type CardID [6]byte

// Info は INT と IDI の結果。
type Info struct {
	SystemKey  [32]byte
	CBCInit    [8]byte
	CASystemID uint16
	IDs        []CardID
}

// ECMResult は ECM への答え。
type ECMResult struct {
	Odd, Even [8]byte
	Code      uint16
}

// KeyUsable は返り値が鍵を使ってよい答えか。契約が無いなどの答えは例外にせず、使わない。
func KeyUsable(code uint16) bool {
	switch code {
	case 0x0000, 0x0200, 0x0400, 0x0800:
		return true
	}
	return false
}

// CardService はデスクランブラが使うカード。同時には 1 つだけ送る (ロックは向こう側)。
type CardService interface {
	Info() (Info, error)
	ECM(ecm []byte) (ECMResult, error)
	// EMM は非同期に渡す。止まらない。
	EMM(payload []byte)
}

const (
	pktSize       = 188
	maxHold       = 8 << 20
	holdTimeout   = 10 * time.Second
	pmtWait       = 3 * time.Second
	patTimeout    = 3 * time.Second
	ecmRetryAfter = 2 * time.Second
)

type esInfo struct {
	streamType uint8
	ecmPID     uint16 // 0 なら CA 無し
}

// keyset は ECM の答えから作った奇数鍵と偶数鍵の MULTI2。
type keyset struct{ odd, even *Multi2 }

// keyGen は溜めている間の鍵の履歴。at 番目のパケットから ks が有効 (nil は鍵なし)。
type keyGen struct {
	at int
	ks *keyset
}

type ecmState struct {
	last     []byte
	cur      *keyset
	gens     []keyGen // 溜めている間だけ付ける
	ok       bool
	answered bool
	retryAt  time.Time
}

// Stream は 1 つの選局の TS を復号する。manager.Processor を満たす。
type Stream struct {
	cs   CardService
	logf func(format string, args ...any)

	decrypt atomic.Bool
	active  bool
	buf     []byte

	asm     map[uint16]*secAsm
	patSeen bool
	patAt   time.Time
	pmtPIDs map[uint16]bool // 値は PMT を受けたか
	es      map[uint16]esInfo
	ecmPIDs map[uint16]bool
	ecm     map[uint16]*ecmState
	emmPID  uint16
	caSys   uint16

	info     Info
	haveInfo bool
	infoFail bool

	holding   bool
	holdStart time.Time
	held      [][]byte
	heldBytes int

	// Debug は調査用。ECM の更新・偶奇の切り替わり・鍵が合わなかった位置をパケットの番号つきで記録に出す。
	Debug       bool
	lastTSC     map[uint16]byte
	packets     atomic.Int64
	undecoded   atomic.Int64
	mismatches  int64
	lastMismLog time.Time
}

// NewStream は選局 1 つぶんの復号を作る。
func NewStream(cs CardService, logf func(format string, args ...any)) *Stream {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Stream{cs: cs, logf: logf}
}

// SetDecrypt は鍵の要る利用者がいるか。いなければカードに問い合わせず、溜めずに流す (8.6)。
func (s *Stream) SetDecrypt(on bool) { s.decrypt.Store(on) }

// Stats は (流したパケット, 復号できなかったパケット)。
func (s *Stream) Stats() (packets, undecoded int64) { return s.packets.Load(), s.undecoded.Load() }

// Close は選局が終わったとき。復号できなかったパケットの数を記録に出す。
func (s *Stream) Close() {
	if u := s.undecoded.Load(); u > 0 {
		s.logf("復号できなかったパケット: %d (全部で %d)", u, s.packets.Load())
	}
}

func (s *Stream) reset() {
	s.active = false
	s.asm = nil
	s.held, s.heldBytes, s.holding = nil, 0, false
}

func (s *Stream) start() {
	s.active = true
	s.asm = map[uint16]*secAsm{0: {}, 1: {}}
	s.patSeen = false
	s.pmtPIDs = map[uint16]bool{}
	s.es = map[uint16]esInfo{}
	s.ecmPIDs = map[uint16]bool{}
	s.ecm = map[uint16]*ecmState{}
	s.emmPID = 0
	s.holding = true
	s.holdStart = time.Now()
	s.held, s.heldBytes = nil, 0
}

// Process は塊を受けて、出せるぶんを返す。パケットの境目は中で揃える。
func (s *Stream) Process(in []byte) []byte {
	s.buf = append(s.buf, in...)
	var out []byte
	pos := 0
	for len(s.buf)-pos >= pktSize {
		if s.buf[pos] != 0x47 || (len(s.buf)-pos > pktSize && s.buf[pos+pktSize] != 0x47) {
			i := bytes.IndexByte(s.buf[pos+1:], 0x47)
			if i < 0 {
				pos = len(s.buf)
				break
			}
			pos += 1 + i
			continue
		}
		out = s.handle(s.buf[pos:pos+pktSize], out)
		pos += pktSize
	}
	s.buf = append(s.buf[:0], s.buf[pos:]...)
	return out
}

// Flush は終わりまで来たとき、溜めているぶんを出す (ファイルの復号)。
func (s *Stream) Flush() []byte {
	if !s.holding {
		return nil
	}
	return s.release(nil)
}

func (s *Stream) handle(pkt []byte, out []byte) []byte {
	s.packets.Add(1)
	if !s.decrypt.Load() {
		if s.active {
			s.reset()
		}
		return append(out, pkt...)
	}
	if !s.active {
		s.start()
	}
	s.feed(pkt)
	if s.holding {
		s.held = append(s.held, append([]byte(nil), pkt...))
		s.heldBytes += pktSize
		if s.ready() || s.heldBytes >= maxHold || time.Since(s.holdStart) > holdTimeout ||
			(!s.patSeen && time.Since(s.holdStart) > patTimeout) {
			return s.release(out)
		}
		return out
	}
	return s.emit(pkt, out)
}

// ready は PAT・PMT と、全部の ECM の最初の答えがそろったか (9.3)。
func (s *Stream) ready() bool {
	if !s.patSeen {
		return false
	}
	for pid, got := range s.pmtPIDs {
		if !got && time.Since(s.patAt) < pmtWait {
			return false
		}
		_ = pid
	}
	for pid := range s.ecmPIDs {
		if st := s.ecm[pid]; st == nil || !st.answered {
			return false
		}
	}
	return true
}

// noteGen は溜めている間、ECM のあとのパケットから ks を使うと覚える。
func (s *Stream) noteGen(st *ecmState, ks *keyset) {
	if s.holding {
		st.gens = append(st.gens, keyGen{at: len(s.held) + 1, ks: ks})
	}
}

// release は溜めたパケットを、到着したときの鍵で復号して出す (9.3)。
// 最初の ECM より前のパケットは、同じ偶奇が最初の ECM をまたいで続いている並びだけを最初の鍵で復号する。
// そうでない (偶奇が替わったより前の) ものは、鍵が分からないのでスクランブルのまま流す。
func (s *Stream) release(out []byte) []byte {
	s.holding = false
	held := s.held
	s.held, s.heldBytes = nil, 0

	// ES ごとに、最初の ECM より前の、最後の同じ偶奇の並びの始まり
	runStart := map[uint16]int{}
	lastTSC := map[uint16]byte{}
	for i, p := range held {
		tsc := p[3] >> 6
		if tsc < 2 {
			continue
		}
		pid := uint16(p[1]&0x1F)<<8 | uint16(p[2])
		es, ok := s.es[pid]
		if !ok || es.ecmPID == 0 {
			continue
		}
		st := s.ecm[es.ecmPID]
		if st == nil || len(st.gens) == 0 || i >= st.gens[0].at {
			continue
		}
		if prev, seen := lastTSC[pid]; !seen || prev != tsc {
			runStart[pid] = i
			lastTSC[pid] = tsc
		}
	}

	for i, p := range held {
		tsc := p[3] >> 6
		if tsc < 2 {
			out = s.emitKS(p, nil, out)
			continue
		}
		pid := uint16(p[1]&0x1F)<<8 | uint16(p[2])
		es, ok := s.es[pid]
		if !ok || es.ecmPID == 0 {
			out = s.emitKS(p, nil, out)
			continue
		}
		st := s.ecm[es.ecmPID]
		var ks *keyset
		if st != nil && len(st.gens) > 0 {
			if i < st.gens[0].at {
				if start, ok := runStart[pid]; ok && i >= start {
					ks = st.gens[0].ks
				}
			} else {
				for _, g := range st.gens {
					if g.at <= i {
						ks = g.ks
					}
				}
			}
		}
		out = s.emitKS(p, ks, out)
	}
	for _, st := range s.ecm {
		st.gens = nil
	}
	return out
}

// emit は 1 パケットを出す。スクランブルされていて鍵があれば復号する。
func (s *Stream) emit(pkt []byte, out []byte) []byte {
	var ks *keyset
	if pkt[3]>>6 >= 2 {
		pid := uint16(pkt[1]&0x1F)<<8 | uint16(pkt[2])
		if es, ok := s.es[pid]; ok && es.ecmPID != 0 {
			if st := s.ecm[es.ecmPID]; st != nil && st.ok {
				ks = st.cur
			}
		}
	}
	return s.emitKS(pkt, ks, out)
}

// emitKS は鍵の組 ks でパケットを復号して出す。ks が nil なら、スクランブルのまま出す。
func (s *Stream) emitKS(pkt []byte, ks *keyset, out []byte) []byte {
	tsc := pkt[3] >> 6
	if s.Debug {
		s.traceTSC(pkt, tsc)
	}
	if tsc < 2 {
		return append(out, pkt...)
	}
	pid := uint16(pkt[1]&0x1F)<<8 | uint16(pkt[2])
	es, ok := s.es[pid]
	if !ok || es.ecmPID == 0 {
		s.undecoded.Add(1)
		return append(out, pkt...)
	}
	if ks == nil {
		// 契約が無いと答えた ECM の ES は「復号できなかった数」に入れない
		if st := s.ecm[es.ecmPID]; st == nil || !st.answered || st.ok {
			s.undecoded.Add(1)
		}
		return append(out, pkt...)
	}
	off := 4
	if pkt[3]&0x20 != 0 {
		off += 1 + int(pkt[4])
	}
	if pkt[3]&0x10 == 0 || off >= pktSize {
		return append(out, pkt...)
	}
	cp := make([]byte, pktSize)
	copy(cp, pkt)
	m := ks.even
	if tsc == 3 {
		m = ks.odd
	}
	m.Decrypt(cp[off:])
	cp[3] &^= 0xC0
	// 映像・音声の PES の先頭では、鍵が合っているかを確かめる (stream_type 0x06 は確かめない)
	if pkt[1]&0x40 != 0 && isAV(es.streamType) {
		if cp[off] != 0 || cp[off+1] != 0 || cp[off+2] != 1 {
			s.mismatches++
			if s.Debug {
				s.logf("[dbg] #%d 鍵が合いません PID 0x%04X tsc=%d ecm=0x%04X", s.packets.Load(), pid, tsc, es.ecmPID)
			}
			if time.Since(s.lastMismLog) > 10*time.Second {
				s.lastMismLog = time.Now()
				s.logf("鍵が合いませんでした (PID 0x%04X)。スクランブルされたまま流します", pid)
			}
			s.undecoded.Add(1)
			return append(out, pkt...)
		}
	}
	return append(out, cp...)
}

func isAV(t uint8) bool {
	switch t {
	case 0x01, 0x02, 0x03, 0x04, 0x0F, 0x11, 0x1B, 0x24:
		return true
	}
	return false
}

// feed は PSI のパケットを読む。
func (s *Stream) feed(pkt []byte) {
	pid := uint16(pkt[1]&0x1F)<<8 | uint16(pkt[2])
	a := s.asm[pid]
	if a == nil || pkt[3]&0x10 == 0 || pkt[3]>>6 != 0 {
		return
	}
	off := 4
	if pkt[3]&0x20 != 0 {
		off += 1 + int(pkt[4])
	}
	if off >= pktSize {
		return
	}
	a.feed(pkt[1]&0x40 != 0, pkt[off:], func(sec []byte) { s.section(pid, sec) })
}

func (s *Stream) section(pid uint16, sec []byte) {
	switch {
	case pid == 0:
		s.onPAT(sec)
	case pid == 1:
		s.onCAT(sec)
	case s.ecmPIDs[pid]:
		s.onECM(pid, sec)
	case pid == s.emmPID && s.emmPID != 0:
		s.onEMM(sec)
	default:
		if _, isPMT := s.pmtPIDs[pid]; isPMT {
			s.onPMT(pid, sec)
		}
	}
}

func (s *Stream) onPAT(sec []byte) {
	if sec[0] != 0x00 || len(sec) < 12 || !crcOK(sec) {
		return
	}
	next := map[uint16]bool{}
	for p := 8; p+4 <= len(sec)-4; p += 4 {
		prog := uint16(sec[p])<<8 | uint16(sec[p+1])
		pid := uint16(sec[p+2]&0x1F)<<8 | uint16(sec[p+3])
		if prog == 0 {
			continue
		}
		next[pid] = s.pmtPIDs[pid]
		if s.asm[pid] == nil {
			s.asm[pid] = &secAsm{}
		}
	}
	if !s.patSeen || len(next) != len(s.pmtPIDs) {
		s.patAt = time.Now()
	}
	s.patSeen = true
	s.pmtPIDs = next
}

func (s *Stream) onCAT(sec []byte) {
	if sec[0] != 0x01 || len(sec) < 12 || !crcOK(sec) {
		return
	}
	for _, d := range descriptors(sec[8 : len(sec)-4]) {
		if d.tag == 0x09 && len(d.data) >= 4 {
			sys := uint16(d.data[0])<<8 | uint16(d.data[1])
			pid := uint16(d.data[2]&0x1F)<<8 | uint16(d.data[3])
			if s.caSys == 0 || sys == s.caSys {
				s.caSys, s.emmPID = sys, pid
				if s.asm[pid] == nil {
					s.asm[pid] = &secAsm{}
				}
			}
		}
	}
}

// caSystemID はカードの CA_system_id (B-CAS は 0x0005)。
func (s *Stream) caSystemID() uint16 {
	if s.ensureInfo() {
		return s.info.CASystemID
	}
	return 0x0005
}

func (s *Stream) ensureInfo() bool {
	if s.haveInfo {
		return true
	}
	if s.infoFail {
		return false
	}
	info, err := s.cs.Info()
	if err != nil {
		s.infoFail = true
		s.logf("カードを使えません: %v", err)
		return false
	}
	s.info, s.haveInfo = info, true
	return true
}

func (s *Stream) onPMT(pid uint16, sec []byte) {
	if sec[0] != 0x02 || len(sec) < 16 || !crcOK(sec) {
		return
	}
	s.pmtPIDs[pid] = true
	ca := s.caSystemID()
	progInfoLen := int(sec[10]&0x0F)<<8 | int(sec[11])
	if 12+progInfoLen > len(sec)-4 {
		return
	}
	progECM := ecmOf(descriptors(sec[12:12+progInfoLen]), ca)
	for p := 12 + progInfoLen; p+5 <= len(sec)-4; {
		st := sec[p]
		epid := uint16(sec[p+1]&0x1F)<<8 | uint16(sec[p+2])
		l := int(sec[p+3]&0x0F)<<8 | int(sec[p+4])
		if p+5+l > len(sec)-4 {
			break
		}
		e := esInfo{streamType: st, ecmPID: progECM}
		if x := ecmOf(descriptors(sec[p+5:p+5+l]), ca); x != 0 {
			e.ecmPID = x
		}
		s.es[epid] = e
		p += 5 + l
	}
	for _, e := range s.es {
		if e.ecmPID != 0 && !s.ecmPIDs[e.ecmPID] {
			s.ecmPIDs[e.ecmPID] = true
			if s.asm[e.ecmPID] == nil {
				s.asm[e.ecmPID] = &secAsm{}
			}
		}
	}
}

func ecmOf(ds []descriptor, caSys uint16) uint16 {
	for _, d := range ds {
		if d.tag == 0x09 && len(d.data) >= 4 && uint16(d.data[0])<<8|uint16(d.data[1]) == caSys {
			return uint16(d.data[2]&0x1F)<<8 | uint16(d.data[3])
		}
	}
	return 0
}

// onECM は ECM の中身が変わったらその場でカードに問い合わせる (同期)。
func (s *Stream) onECM(pid uint16, sec []byte) {
	if (sec[0] != 0x82 && sec[0] != 0x83) || len(sec) < 8+4+1 {
		return
	}
	content := sec[8 : len(sec)-4]
	if len(content) < 1 || len(content) > 255 {
		return
	}
	st := s.ecm[pid]
	if st == nil {
		st = &ecmState{}
		s.ecm[pid] = st
	}
	same := bytes.Equal(st.last, content)
	if same && (st.ok || time.Now().Before(st.retryAt)) {
		return
	}
	st.last = append(st.last[:0], content...)
	st.answered = true
	st.retryAt = time.Now().Add(ecmRetryAfter)
	if !s.ensureInfo() {
		st.ok, st.cur = false, nil
		s.noteGen(st, nil)
		return
	}
	res, err := s.cs.ECM(content)
	if err != nil {
		st.ok, st.cur = false, nil
		s.noteGen(st, nil)
		s.logf("ECM を問い合わせられません (PID 0x%04X): %v", pid, err)
		return
	}
	if !KeyUsable(res.Code) {
		st.ok, st.cur = false, nil
		s.noteGen(st, nil)
		return
	}
	if s.Debug {
		s.logf("[dbg] #%d ECM PID 0x%04X 更新 len=%d code=0x%04X odd=%x even=%x 溜め=%v", s.packets.Load(), pid, len(content), res.Code, res.Odd[:2], res.Even[:2], s.holding)
	}
	ks := &keyset{odd: NewMulti2(s.info.SystemKey, s.info.CBCInit), even: NewMulti2(s.info.SystemKey, s.info.CBCInit)}
	ks.odd.SetScrambleKey(res.Odd)
	ks.even.SetScrambleKey(res.Even)
	st.cur, st.ok = ks, true
	s.noteGen(st, ks)
}

// onEMM は自分のカードの番号に合ったペイロードだけをカードに渡す (9.4)。
func (s *Stream) onEMM(sec []byte) {
	if sec[0] != 0x84 || len(sec) < 3+4 || !s.ensureInfo() {
		return
	}
	end := len(sec) - 4
	for p := 3; p+7 <= end; {
		n := 7 + int(sec[p+6])
		if p+n > end {
			break
		}
		payload := sec[p : p+n]
		p += n
		if len(payload) < 13 || len(payload) > 255 {
			continue
		}
		for _, id := range s.info.IDs {
			if bytes.Equal(payload[:6], id[:]) {
				s.cs.EMM(append([]byte(nil), payload...))
				break
			}
		}
	}
}

// traceTSC は ES ごとの偶奇 (transport_scrambling_control) が変わった位置を記録に出す (調査用)。
func (s *Stream) traceTSC(pkt []byte, tsc byte) {
	pid := uint16(pkt[1]&0x1F)<<8 | uint16(pkt[2])
	if es, ok := s.es[pid]; !ok || es.ecmPID == 0 {
		return
	}
	if s.lastTSC == nil {
		s.lastTSC = map[uint16]byte{}
	}
	if prev, seen := s.lastTSC[pid]; !seen || prev != tsc {
		s.lastTSC[pid] = tsc
		s.logf("[dbg] #%d PID 0x%04X tsc %d→%d", s.packets.Load(), pid, prev, tsc)
	}
}
