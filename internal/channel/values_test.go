package channel

import "testing"

func TestValues_表の代表と境界(t *testing.T) {
	cases := []struct {
		in      string
		sys     uint32
		freqNo  int
		slot    int
		freqKHz uint32
		stream  int
	}{
		{"T13", SystemT, 63, 0, 473143, 0},
		{"T27", SystemT, 77, 0, 557143, 0}, // 仕様書の例 T27 → 77
		{"T62", SystemT, 112, 0, 767143, 0},
		{"C13", SystemT, 3, 0, 111143, 0},
		{"C22", SystemT, 12, 0, 167143, 0}, // C22 は + 2000
		{"C23", SystemT, 22, 0, 225143, 0},
		{"C63", SystemT, 62, 0, 465143, 0},
		{"BS01", SystemS, 0, 0, 1049480, 0},
		{"BS15", SystemS, 7, 0, 1318000, 0},
		{"BS15:16625", SystemS, 7, 16625, 1318000, 0},
		{"BS23", SystemS, 11, 0, 1049480 + 38360*11, 0},
		{"CS02", SystemS, 12, 0, 1613000, 0},
		{"CS24", SystemS, 23, 0, 1613000 + 40000*11, 0},
		{"CS04:12", SystemS, 13, 12, 1653000, 0},
		{"QV1", SystemC, 0, 0, 93000, 0},
		{"QV3", SystemC, 2, 0, 105000, 0},
		{"QV4", SystemC, 13, 0, 173000, 0},
		{"QV7", SystemC, 16, 0, 191000, 0},
		{"QV8", SystemC, 17, 0, 195000, 0},
		{"QV12", SystemC, 21, 0, 219000, 0},
		{"QC13", SystemC, 3, 0, 111000, 0},
		{"QC22", SystemC, 12, 0, 167000, 0},
		{"QC23", SystemC, 22, 0, 225000, 0},
		{"QC30:2", SystemC, 29, 0, 225000 + 7*6000, 2},
		{"QC63", SystemC, 62, 0, 225000 + 40*6000, 0},
		{"QU13", SystemC, 63, 0, 473000, 0},
		{"QU62", SystemC, 112, 0, 473000 + 49*6000, 0},
		{"H01", SystemJ83, 0, 0, 93250, 0},
		{"H44", SystemJ83, 0, 0, 93250 + 43*6500, 0},
		{"H105", SystemJ83, 0, 0, 93250 + 104*6500, 0},
	}
	for _, c := range cases {
		ch, err := Parse(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		v := ch.Values()
		if v.System != c.sys || v.FreqNo != c.freqNo || v.Slot != c.slot || v.FreqKHz != c.freqKHz || v.Stream != c.stream {
			t.Errorf("%s: got %+v", c.in, v)
		}
	}
}

func TestValues_衛星のSTREAM_IDはTSIDか0(t *testing.T) {
	ch, _ := Parse("BS15:16625")
	if ch.Values().StreamID != 16625 {
		t.Error("StreamID")
	}
	ch, _ = Parse("BS15")
	if ch.Values().StreamID != 0 {
		t.Error("StreamID は 0")
	}
}

func TestShiftedFreqKHz_SKYのずらした周波数(t *testing.T) {
	cases := []struct {
		in   string
		want uint32
	}{
		{"H01", 0},
		{"H10", 0},
		{"H11", 93250 + 10*6500 + 6500},
		{"H12", 93250 + 11*6500 + 6500},
		{"H13", 0},
		{"H25", 0},
		{"H26", 93250 + 25*6500 + 6500},
		{"H27", 93250 + 26*6500 + 6500},
		{"H28", 93250 + 27*6500 + 6500},
		{"H29", 0},
		{"H43", 0},
		{"H44", 93250 + 43*6500 + 6500},
		{"H45", 0},
		{"T27", 0},
		{"BS15", 0},
		{"QC30", 0},
	}
	for _, c := range cases {
		ch, err := Parse(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if got := ch.ShiftedFreqKHz(); got != c.want {
			t.Errorf("%s: %d (want %d)", c.in, got, c.want)
		}
	}
}
