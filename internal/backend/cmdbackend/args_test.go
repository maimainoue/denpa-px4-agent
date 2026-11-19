package cmdbackend

import (
	"reflect"
	"testing"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/devices"
)

func TestBuildArgs(t *testing.T) {
	recpt1 := &devices.Def{
		Command: []string{"recpt1", "--device", "{device}", "{lnb}", "{channel}", "-", "-"},
		LnbArg: map[string][]string{
			"15v": {"--lnb", "15"},
			"11v": {"--lnb", "11"},
			"":    {},
		},
	}
	nasneHTTP := &devices.Def{
		Command: []string{"curl", "-sfN", "http://nasne.local:8301/tuner/{channel}{sidQuery}"},
	}
	nasneLocal := &devices.Def{
		Command:    []string{"/usr/local/sbin/nasne-recpt1", "{channel}", "-", "-"},
		ServiceArg: []string{"--sid", "{service}"},
		TsidArg:    []string{"--tsid", "{tsid}"},
	}
	px4rec := &devices.Def{Command: []string{"px4rec", "{channel}", "{service}", "-"}}
	fake := &devices.Def{Command: []string{"/fake", "{type}", "{channel}"}}

	cases := []struct {
		name string
		def  *devices.Def
		p    Params
		want []string
	}{
		{"{lnb} は複数の引数に展開される", recpt1,
			Params{Device: "/dev/pt3video2", Channel: []string{"BS15_0"}, Lnb: "15v", TSID: -1},
			[]string{"recpt1", "--device", "/dev/pt3video2", "--lnb", "15", "BS15_0", "-", "-"}},
		{"lnbArg が空なら {lnb} は消える", recpt1,
			Params{Device: "/dev/pt3video2", Channel: []string{"27"}, Lnb: "", TSID: -1},
			[]string{"recpt1", "--device", "/dev/pt3video2", "27", "-", "-"}},
		{"lnbArg に無い設定でも消える", &devices.Def{Command: []string{"x", "{lnb}", "{channel}"}},
			Params{Channel: []string{"27"}, Lnb: "15v", TSID: -1},
			[]string{"x", "27"}},
		{"{channel} は 2 つ以上の引数に展開される", px4rec,
			Params{Channel: []string{"C", "30", "--tsmf", "2"}, TSID: -1},
			[]string{"px4rec", "C", "30", "--tsmf", "2", "-"}},
		{"局が無ければ {service} だけの要素は消える", px4rec,
			Params{Channel: []string{"T", "27"}, TSID: -1},
			[]string{"px4rec", "T", "27", "-"}},
		{"局があれば {service} に入る", px4rec,
			Params{Channel: []string{"T", "27"}, Service: 1024, TSID: -1},
			[]string{"px4rec", "T", "27", "1024", "-"}},
		{"{sidQuery} は要素の中に埋め込める (局あり)", nasneHTTP,
			Params{Channel: []string{"27"}, Service: 23608, TSID: -1},
			[]string{"curl", "-sfN", "http://nasne.local:8301/tuner/27?sid=23608"}},
		{"{sidQuery} は局が無ければ空", nasneHTTP,
			Params{Channel: []string{"27"}, TSID: -1},
			[]string{"curl", "-sfN", "http://nasne.local:8301/tuner/27"}},
		{"serviceArg は局があるときだけ末尾に足す", nasneLocal,
			Params{Channel: []string{"27"}, Service: 23608, TSID: -1},
			[]string{"/usr/local/sbin/nasne-recpt1", "27", "-", "-", "--sid", "23608"}},
		{"serviceArg は局が無ければ足さない", nasneLocal,
			Params{Channel: []string{"27"}, TSID: -1},
			[]string{"/usr/local/sbin/nasne-recpt1", "27", "-", "-"}},
		{"tsidArg は TSID があるときだけ足す", nasneLocal,
			Params{Channel: []string{"BS15_0"}, Service: 211, TSID: 16625},
			[]string{"/usr/local/sbin/nasne-recpt1", "BS15_0", "-", "-", "--sid", "211", "--tsid", "16625"}},
		{"TSID 0 も渡す", nasneLocal,
			Params{Channel: []string{"BS15_0"}, TSID: 0},
			[]string{"/usr/local/sbin/nasne-recpt1", "BS15_0", "-", "-", "--tsid", "0"}},
		{"FAKE_TUNE は種別とチャンネル名をそのまま渡す", fake,
			Params{Type: "BS", Channel: []string{"BS15:16625"}, TSID: 16625},
			[]string{"/fake", "BS", "BS15:16625"}},
	}
	for _, c := range cases {
		got := BuildArgs(c.def, c.p)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got  %q\n want %q", c.name, got, c.want)
		}
	}
}
