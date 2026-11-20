package channel

// 方式のビット。chardev の PTX_SET_SYSTEM_MODE と host の systems で同じ値 (仕様書 4.3・5.2)。
const (
	SystemT   uint32 = 0x10 // ISDB-T
	SystemS   uint32 = 0x20 // ISDB-S
	SystemC   uint32 = 0x40 // ISDB-C
	SystemJ83 uint32 = 0x80 // J.83B
)

// Values は選局の値 (仕様書 7.3 の表)。
type Values struct {
	System uint32
	// FreqNo と Slot は chardev の PTX_SET_CHANNEL。SKY では使わない。
	FreqNo int
	Slot   int
	// FreqKHz は host の SET_PARAMS と、chardev の PTXT_SET_PARAMS の中心周波数。
	FreqKHz uint32
	// Stream は ISDB-C の TSMF の k 本目。
	Stream int
	// StreamID は衛星の host の STREAM_ID (`:TSID` の値か 0)。
	StreamID int
}

// Values は読んだチャンネル名から選局の値を出す。
func (c Channel) Values() Values {
	n := c.Number
	v := Values{}
	switch c.Prefix {
	case "T":
		v.System = SystemT
		v.FreqNo = n + 50
		v.FreqKHz = uint32(395143 + n*6000)
	case "C":
		v.System = SystemT
		if n <= 22 {
			v.FreqNo = n - 10
		} else {
			v.FreqNo = n - 1
		}
		v.FreqKHz = uint32(93143 + v.FreqNo*6000)
		if n == 22 {
			v.FreqKHz += 2000
		}
	case "BS":
		v.System = SystemS
		v.FreqNo = (n - 1) / 2
		v.FreqKHz = uint32(1049480 + 38360*v.FreqNo)
		v.Slot = max(c.TSID, 0)
		v.StreamID = v.Slot
	case "CS":
		v.System = SystemS
		v.FreqNo = 12 + (n-2)/2
		v.FreqKHz = uint32(1613000 + 40000*(v.FreqNo-12))
		v.Slot = max(c.TSID, 0)
		v.StreamID = v.Slot
	case "QV", "QC", "QU":
		v.System = SystemC
		v.Stream = max(c.Stream, 0)
		switch {
		case c.Prefix == "QV" && n <= 3:
			v.FreqNo = n - 1
			v.FreqKHz = uint32(93000 + v.FreqNo*6000)
		case c.Prefix == "QC" && n <= 22:
			v.FreqNo = n - 10
			v.FreqKHz = uint32(111000 + (v.FreqNo-3)*6000)
			if n == 22 {
				v.FreqKHz = 167000
			}
		case c.Prefix == "QV":
			v.FreqNo = n + 9
			if n <= 7 {
				v.FreqKHz = uint32(173000 + (n-4)*6000)
			} else {
				v.FreqKHz = uint32(195000 + (n-8)*6000)
			}
		case c.Prefix == "QC":
			v.FreqNo = n - 1
			v.FreqKHz = uint32(225000 + (v.FreqNo-22)*6000)
		default: // QU
			v.FreqNo = n + 50
			v.FreqKHz = uint32(473000 + (v.FreqNo-63)*6000)
		}
	case "H":
		v.System = SystemJ83
		v.FreqKHz = uint32(93250 + (n-1)*6500)
	}
	return v
}

// ShiftedFreqKHz は SKY でずらした周波数を持つチャンネルの、ずらした周波数 (仕様書 7.2)。
// H11〜H12・H26〜H28・H44 が対象で、それ以外は 0。ずらす量は +6500 kHz。
func (c Channel) ShiftedFreqKHz() uint32 {
	if c.Prefix != "H" {
		return 0
	}
	switch c.Number {
	case 11, 12, 26, 27, 28, 44:
		return c.Values().FreqKHz + 6500
	}
	return 0
}
