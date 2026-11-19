package b61

// MMT-SI の読み (docs/b61.md の 6)。

type siKind int

const (
	siPLT siKind = iota
	siCAT
	siMPT
	siECM
	siEMM
)

// siBuf は信号の packet id 1 つ。断片を溜めて、メッセージとして読む。
type siBuf struct {
	pid     uint16
	kind    siKind
	version int
	buf     []byte
}

// 記述子のタグの長さのバイト数 (docs/b61.md の 6.3)。
func descLenBytes(tag uint16) int {
	switch {
	case tag >= 0x4000 && tag <= 0x6FFF:
		return 2
	case tag >= 0x7000 && tag <= 0x7FFF:
		return 4
	case tag >= 0xF000:
		return 2
	}
	return 1
}

// descriptor は 1 つの記述子。
type descriptor struct {
	tag  uint16
	body []byte
}

// readDescriptors は b を記述子の並びとして読む。壊れていたら、読めたところまで。
func readDescriptors(b []byte) []descriptor {
	var out []descriptor
	for len(b) >= 3 {
		tag := uint16(b[0])<<8 | uint16(b[1])
		n := descLenBytes(tag)
		b = b[2:]
		if len(b) < n {
			break
		}
		l := 0
		for i := 0; i < n; i++ {
			l = l<<8 | int(b[i])
		}
		b = b[n:]
		if len(b) < l {
			break
		}
		out = append(out, descriptor{tag, b[:l]})
		b = b[l:]
	}
	return out
}

// caPID は CA 記述子 (0x8004) から A-CAS の packet id を取る。
func caPID(d descriptor) (uint16, bool) {
	if d.tag != 0x8004 || len(d.body) < 5 {
		return 0, false
	}
	if uint16(d.body[0])<<8|uint16(d.body[1]) != caSystemACAS || d.body[2] != 0x00 {
		return 0, false
	}
	return uint16(d.body[3])<<8 | uint16(d.body[4]), true
}

// onSI は信号のパケット (ペイロード型 2) 1 つ。
func (s *Stream) onSI(si *siBuf, payload []byte) {
	if len(payload) < 2 {
		return
	}
	frag := payload[0] >> 6
	data := payload[2:]
	if frag == 0 || frag == 1 {
		si.buf = si.buf[:0]
	}
	if frag == 0 {
		s.onMessage(si, data)
		return
	}
	si.buf = append(si.buf, data...)
	if frag == 3 {
		msg := si.buf
		si.buf = nil
		s.onMessage(si, msg)
	}
}

func (s *Stream) onMessage(si *siBuf, b []byte) {
	if len(b) < 5 {
		return
	}
	id := int(b[0])<<8 | int(b[1])
	length := int(b[3])<<8 | int(b[4])
	b = b[5:]
	if id == 0x0000 || id == 0x8003 { // 長さが 32 ビット
		if len(b) < 2 {
			return
		}
		length = length<<16 | int(b[0])<<8 | int(b[1])
		b = b[2:]
	}
	if length < 0 || len(b) < length {
		return
	}
	b = b[:length]
	switch si.kind {
	case siPLT, siMPT:
		if id == 0x0000 {
			s.onPA(si, b)
		}
	case siECM, siEMM:
		s.onM2(si, b)
	case siCAT:
		if id == 0x8001 {
			s.onCA(b)
		}
	}
}

// onCA は CA メッセージ (表 0x86 の CAT)。EMM の packet id を決める。
func (s *Stream) onCA(b []byte) {
	if len(b) < 4 {
		return
	}
	n := int(b[2]&0x0F)<<8 | int(b[3])
	if b[0] != 0x86 || len(b) < 4+n {
		return
	}
	var emm uint16
	found := false
	for _, d := range readDescriptors(b[4 : 4+n]) {
		if pid, ok := caPID(d); ok {
			emm, found = pid, true
			break
		}
	}
	for pid, e := range s.si {
		if e.kind == siEMM && (!found || pid != emm) {
			delete(s.si, pid)
		}
	}
	if found {
		if _, ok := s.si[emm]; !ok {
			s.si[emm] = &siBuf{pid: emm, kind: siEMM, version: -1}
		}
	}
}

// onM2 は M2 メッセージ (ECM の表 0x82・EMM の表 0x84)。
func (s *Stream) onM2(si *siBuf, b []byte) {
	if len(b) < 3 {
		return
	}
	n := int(b[1]&0x0F)<<8 | int(b[2])
	if len(b) < 3+n || n < 4 {
		return
	}
	body := b[3 : 3+n-4] // 末尾の CRC32 は読まない
	switch {
	case si.kind == siECM && b[0] == 0x82:
		s.onECM(si, body)
	case si.kind == siEMM && b[0] == 0x84:
		s.onEMM(body)
	}
}

// onPA は PA メッセージ。先頭の表の数が 0 のものだけ読む。
func (s *Stream) onPA(si *siBuf, b []byte) {
	if len(b) < 1 || b[0] != 0 {
		return
	}
	b = b[1:]
	if len(b) < 4 {
		return
	}
	tableID, version := b[0], int(b[1])
	n := int(b[2])<<8 | int(b[3])
	b = b[4:]
	if len(b) < n || si.version == version {
		return
	}
	si.version = version
	table := b[:n]
	switch {
	case si.kind == siPLT && tableID == 0x80:
		s.onPLT(table)
	case si.kind == siMPT && tableID == 0x20:
		s.onMPT(si, table)
	}
}

// onPLT は番組の一覧。サービス ID と MPT の packet id。
func (s *Stream) onPLT(b []byte) {
	if len(b) < 1 {
		return
	}
	count := int(b[0])
	b = b[1:]
	seen := map[uint16]bool{}
	for i := 0; i < count; i++ {
		if len(b) < 1 || b[0] != 2 || len(b) < 1+2+1+2 {
			return
		}
		serviceID := uint16(b[1])<<8 | uint16(b[2])
		if b[3] != 0x00 {
			return
		}
		pid := uint16(b[4])<<8 | uint16(b[5])
		b = b[6:]
		seen[pid] = true
		if p, ok := s.programs[pid]; ok {
			p.serviceID = serviceID
		} else {
			s.programs[pid] = &program{serviceID: serviceID, ecmPID: 0xFFFF, assets: map[uint16]struct{}{}}
			s.si[pid] = &siBuf{pid: pid, kind: siMPT, version: -1}
		}
	}
	for pid, p := range s.programs {
		if !seen[pid] {
			s.removeProgram(pid, p)
		}
	}
}

func (s *Stream) removeProgram(pid uint16, p *program) {
	for a := range p.assets {
		delete(s.assets, a)
	}
	delete(s.si, pid)
	delete(s.programs, pid)
	s.dropECM(p.ecmPID)
}

// onMPT は番組の中身。ECM の packet id と、スクランブルされた asset。
func (s *Stream) onMPT(si *siBuf, b []byte) {
	prog := s.programs[si.pid]
	if prog == nil || len(b) < 1+1 {
		return
	}
	if b[0]&3 != 0 {
		return
	}
	b = b[1:]
	idLen := int(b[0])
	if idLen != 2 || len(b) < 1+idLen+2 {
		return
	}
	b = b[1+idLen:]
	dl := int(b[0])<<8 | int(b[1])
	b = b[2:]
	if len(b) < dl {
		return
	}
	prev := prog.ecmPID
	prog.ecmPID = 0xFFFF
	prog.mptReceived = true
	for _, d := range readDescriptors(b[:dl]) {
		if pid, ok := caPID(d); ok {
			prog.ecmPID = pid
		}
	}
	b = b[dl:]
	if len(b) < 1 {
		return
	}
	b = b[1:] // asset の数。数えずに、読めるだけ読む
	for a := range prog.assets {
		delete(s.assets, a)
	}
	prog.assets = map[uint16]struct{}{}
	for len(b) >= 1+4+1 {
		idLen := int(b[5])
		b = b[6:]
		if len(b) < idLen+4+1+1 {
			break
		}
		b = b[idLen+4+1:] // asset の ID・型・予約
		if b[0] != 1 {    // 場所は 1 つだけ
			break
		}
		b = b[1:]
		if len(b) < 3 || b[0] != 0x00 {
			break
		}
		pid := uint16(b[1])<<8 | uint16(b[2])
		b = b[3:]
		if len(b) < 2 {
			break
		}
		al := int(b[0])<<8 | int(b[1])
		b = b[2:]
		if len(b) < al {
			break
		}
		scrambled := true
		for _, d := range readDescriptors(b[:al]) {
			if id, ok := caPID(d); ok && id == 0xFFFF {
				scrambled = false
			}
		}
		b = b[al:]
		if scrambled {
			s.assets[pid] = &asset{ecmPID: prog.ecmPID}
			prog.assets[pid] = struct{}{}
		}
	}
	if prev != prog.ecmPID {
		s.addECM(prog.ecmPID)
		s.dropECM(prev)
	}
	s.checkNoScramble()
}
