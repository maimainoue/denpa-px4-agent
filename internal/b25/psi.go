package b25

// secAsm は 1 つの PID のセクションを組み立てる。
type secAsm struct {
	buf  []byte
	need int
}

func (a *secAsm) feed(pusi bool, payload []byte, emit func([]byte)) {
	if pusi {
		if len(payload) < 1 {
			return
		}
		ptr := int(payload[0])
		if ptr+1 > len(payload) {
			a.buf = nil
			return
		}
		if len(a.buf) > 0 && ptr > 0 {
			a.buf = append(a.buf, payload[1:1+ptr]...)
			if len(a.buf) >= a.need {
				emit(a.buf[:a.need])
			}
		}
		a.buf = nil
		payload = payload[1+ptr:]
		for len(payload) >= 3 && payload[0] != 0xFF {
			length := 3 + (int(payload[1]&0x0F)<<8 | int(payload[2]))
			if len(payload) >= length {
				emit(append([]byte(nil), payload[:length]...))
				payload = payload[length:]
				continue
			}
			a.buf = append([]byte(nil), payload...)
			a.need = length
			return
		}
		return
	}
	if len(a.buf) == 0 {
		return
	}
	a.buf = append(a.buf, payload...)
	if len(a.buf) >= a.need {
		emit(a.buf[:a.need])
		a.buf = nil
	}
}

type descriptor struct {
	tag  uint8
	data []byte
}

func descriptors(b []byte) []descriptor {
	var out []descriptor
	for len(b) >= 2 {
		l := int(b[1])
		if 2+l > len(b) {
			break
		}
		out = append(out, descriptor{tag: b[0], data: b[2 : 2+l]})
		b = b[2+l:]
	}
	return out
}

var crcTable = func() [256]uint32 {
	var t [256]uint32
	for i := range t {
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

// crcOK は MPEG-2 の CRC32 (セクション全体を通すと 0)。
func crcOK(sec []byte) bool {
	crc := uint32(0xFFFFFFFF)
	for _, b := range sec {
		crc = crc<<8 ^ crcTable[byte(crc>>24)^b]
	}
	return crc == 0
}

// crc32mpeg は CRC32 を付けるときに使う (試験用)。
func crc32mpeg(b []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, x := range b {
		crc = crc<<8 ^ crcTable[byte(crc>>24)^x]
	}
	return crc
}
