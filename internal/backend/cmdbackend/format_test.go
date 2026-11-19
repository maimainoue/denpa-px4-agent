package cmdbackend

import (
	"errors"
	"reflect"
	"testing"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/channel"
)

func mustParse(t *testing.T, s string) channel.Channel {
	t.Helper()
	ch, err := channel.Parse(s)
	if err != nil {
		t.Fatalf("%s: %v", s, err)
	}
	return ch
}

// 仕様書 6.2 の表の全部。want が nil のものは断る。
func TestFormatChannel_表の全部(t *testing.T) {
	type row map[string][]string // 入力 → 期待。nil は断る
	table := map[string]row{
		"recpt1": {
			"T27":        {"27"},
			"C30":        {"C30"},
			"BS15":       {"BS15_0"},
			"BS01":       {"BS1_0"},
			"BS15:16625": nil,
			"CS02":       {"CS2"},
			"CS24":       {"CS24"},
			"QC30:2":     nil,
			"QC30":       nil,
			"H44":        nil,
		},
		"recisdb": {
			"T27":        {"T27"},
			"C30":        {"C30"},
			"BS15":       {"BS15_0"},
			"BS01":       {"BS01_0"},
			"BS15:16625": nil,
			"CS02":       {"CS02"},
			"QC30:2":     {"ISDBC-C30_2"},
			"QC30":       {"ISDBC-C30_0"},
			"H44":        {"J83B-H044"},
		},
		"px4rec": {
			"T27":        {"T", "27"},
			"C30":        nil,
			"BS15":       {"S", "7"},
			"BS01":       {"S", "0"},
			"BS23":       {"S", "11"},
			"BS15:16625": nil,
			"CS02":       {"S", "12"},
			"CS24":       {"S", "23"},
			"QC30:2":     {"C", "30", "--tsmf", "2"},
			"QC30":       {"C", "30"},
			"QV1":        nil,
			"H44":        {"J", "44"},
		},
		"denpa": {
			"T27":        {"T27"},
			"BS15:16625": {"BS15:16625"},
			"QC30:2":     {"QC30:2"},
			"H44":        {"H44"},
		},
	}
	for format, rows := range table {
		for in, want := range rows {
			got, err := FormatChannel(format, mustParse(t, in), false)
			if want == nil {
				if err == nil {
					t.Errorf("%s %s: 断るはずが %v", format, in, got)
				}
				continue
			}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("%s %s: got %v, %v want %v", format, in, got, err, want)
			}
		}
	}
}

func TestFormatChannel_TSIDを断る文言(t *testing.T) {
	for _, f := range []string{"recpt1", "recisdb", "px4rec"} {
		_, err := FormatChannel(f, mustParse(t, "BS15:16625"), false)
		var fe *FormatError
		if !errors.As(err, &fe) || !fe.TSID || err.Error() != "このコマンドでは TSID を指定できません" {
			t.Errorf("%s: %v", f, err)
		}
	}
}

func TestFormatChannel_tsidArgがあればTSIDは別の引数で渡す(t *testing.T) {
	got, err := FormatChannel("recpt1", mustParse(t, "BS15:16625"), true)
	if err != nil || !reflect.DeepEqual(got, []string{"BS15_0"}) {
		t.Fatalf("got %v, %v", got, err)
	}
}
