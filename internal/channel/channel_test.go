package channel

import "testing"

func TestParse_読める名前(t *testing.T) {
	cases := []struct {
		in     string
		typ    Type
		name   string
		str    string
		tsid   int
		stream int
	}{
		{"T13", GR, "T13", "T13", -1, -1},
		{"T27", GR, "T27", "T27", -1, -1},
		{"T62", GR, "T62", "T62", -1, -1},
		{"C13", GR, "C13", "C13", -1, -1},
		{"C63", GR, "C63", "C63", -1, -1},
		{"BS01", BS, "BS01", "BS01", -1, -1},
		{"BS15", BS, "BS15", "BS15", -1, -1},
		{"BS15:16625", BS, "BS15", "BS15:16625", 16625, -1},
		{"BS23", BS, "BS23", "BS23", -1, -1},
		{"bs1", BS, "BS01", "BS01", -1, -1},
		{"CS02", CS, "CS02", "CS02", -1, -1},
		{"CS24:0", CS, "CS24", "CS24:0", 0, -1},
		{"QV1", CATV, "QV1", "QV1", -1, -1},
		{"QV12", CATV, "QV12", "QV12", -1, -1},
		{"QC13", CATV, "QC13", "QC13", -1, -1},
		{"QC30:2", CATV, "QC30", "QC30:2", -1, 2},
		{"QC30:14", CATV, "QC30", "QC30:14", -1, 14},
		{"QU62", CATV, "QU62", "QU62", -1, -1},
		{"H01", SKY, "H01", "H01", -1, -1},
		{"H44", SKY, "H44", "H44", -1, -1},
		{"H105", SKY, "H105", "H105", -1, -1},
		{" T27 ", GR, "T27", "T27", -1, -1},
	}
	for _, c := range cases {
		got, err := Parse(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got.Type != c.typ || got.Name() != c.name || got.String() != c.str || got.TSID != c.tsid || got.Stream != c.stream {
			t.Errorf("%q: got %+v (%s / %s)", c.in, got, got.Name(), got.String())
		}
	}
}

func TestParse_読めない名前は断る(t *testing.T) {
	bad := []string{
		"", "T", "T12", "T63", "C12", "C64", "T27:1", "C30:1",
		"BS02", "BS25", "BS00", "CS01", "CS26", "CS00",
		"QV0", "QV13", "QC12", "QC64", "QU63", "QC30:15", "H00", "H106", "H44:1",
		"X27", "BS15:abc", "BS15:70000", "T-1",
	}
	for _, in := range bad {
		_, err := Parse(in)
		if err == nil {
			t.Errorf("%q: 読めてしまった", in)
			continue
		}
		if _, ok := err.(*Error); !ok {
			t.Errorf("%q: *Error ではない (%T)", in, err)
		}
	}
}

func TestParse_BS07とBS17は4K8Kなので断る(t *testing.T) {
	for _, in := range []string{"BS07", "BS17", "BS17:16625"} {
		_, err := Parse(in)
		if err == nil {
			t.Fatalf("%s: 読めてしまった", in)
		}
		if got := err.Error(); got != in+": BS07 と BS17 は 4K/8K のため選局できません" {
			t.Errorf("%s: 文言 %q", in, got)
		}
	}
}

func TestParse_読めないときの文言(t *testing.T) {
	_, err := Parse("ZZ9")
	if err == nil || err.Error() != "ZZ9: チャンネル名を読めません" {
		t.Fatalf("got %v", err)
	}
}
