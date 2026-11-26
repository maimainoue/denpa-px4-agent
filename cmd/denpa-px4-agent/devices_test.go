package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/app"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/card"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/devices"
)

func TestReceiversOf_同じ種別が続くものはまとめる(t *testing.T) {
	r := func(types ...string) devices.Receiver { return devices.Receiver{Types: types} }
	for _, c := range []struct {
		rs   []devices.Receiver
		want string
	}{
		{nil, "-"},
		{[]devices.Receiver{r("GR")}, "1 基: GR"},
		{[]devices.Receiver{r("BS", "CS"), r("BS", "CS"), r("GR"), r("GR")}, "4 基: BS/CS×2, GR×2"},
		{[]devices.Receiver{r("GR", "BS"), r("BS"), r("GR", "BS")}, "3 基: GR/BS, BS, GR/BS"}, // 離れたものはまとめない
		{[]devices.Receiver{r("GR"), r("GR"), r("GR"), r("GR"), r("GR")}, "5 基: GR×5"},
	} {
		if got := receiversOf(devices.Def{Receivers: c.rs}); got != c.want {
			t.Errorf("%v: %q, want %q", c.rs, got, c.want)
		}
	}
}

func TestLnbOf_書いていない_給電できない_電圧(t *testing.T) {
	for _, c := range []struct {
		lnb  []int
		want string
	}{{nil, "-"}, {[]int{}, "なし"}, {[]int{15}, "15v"}, {[]int{11, 15}, "11v/15v"}} {
		if got := lnbOf(devices.Def{LNB: c.lnb}); got != c.want {
			t.Errorf("%v: %q, want %q", c.lnb, got, c.want)
		}
	}
}

func TestPrintDevices_定義と検出とtuners_json(t *testing.T) {
	rep := app.DevicesReport{
		Defs:       devices.Builtin().All(),
		TunersFile: "/c/tuners.json",
		Found:      true,
		Configured: []backend.TunerSpec{{Name: "torne0", Types: []string{"GR"}, Device: "px4host:{D3C0AEC3-0FC9-4769-8513-8323CEFD792F}:0:0"}, {Name: "off", Types: []string{"BS", "CS"}, Disabled: true, Lnb: "15v"}},
	}
	var b bytes.Buffer
	printDevices(&b, rep)
	out := b.String()
	for _, want := range []string{
		"機材の定義 (",
		"PLEX PX-W3U4", "chardev+host", "0511:083f", "4 基: BS/CS×2, GR×2", "15v", "{aa8de3fc-004d-49a4-b976-4250f15a976d}", // 機種名・経路・USB・受信機・LNB・GUID
		"Sony CECH-ZD1 J", "{D3C0AEC3-0FC9-4769-8513-8323CEFD792F}",
		"自動検出がありません", // HasDetector が偽 = Windows と macOS
		"tuners.json (/c/tuners.json)", "torne0  px4host:", "off  -  (BS/CS)  無効  lnb 15v",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("出力に %q がありません:\n%s", want, out)
		}
	}
}

func TestPrintDevices_検出した機材_探せない_tuners_jsonが無い(t *testing.T) {
	var b bytes.Buffer
	printDevices(&b, app.DevicesReport{
		HasDetector: true,
		Detected:    []backend.TunerSpec{{Name: "px4-0", Types: []string{"GR"}, Device: "px4:/dev/px4video2"}},
		DetectErrs:  []string{"/dev を読めません"},
		TunersFile:  "/config/tuners.json",
	})
	out := b.String()
	for _, want := range []string{"px4-0  px4:/dev/px4video2  (GR)", "探せません: /dev を読めません", "ファイルがありません"} {
		if !strings.Contains(out, want) {
			t.Errorf("出力に %q がありません:\n%s", want, out)
		}
	}
	if strings.Contains(out, "自動検出がありません") {
		t.Errorf("検出がある OS なのに、検出が無いと書いた:\n%s", out)
	}

	b.Reset()
	printDevices(&b, app.DevicesReport{HasDetector: true, TunersFile: "x"})
	if !strings.Contains(b.String(), "(見つかりません)") {
		t.Errorf("1 台も無いとき:\n%s", b.String())
	}
}

func TestPrintCard_読めたリーダーと読めないリーダー(t *testing.T) {
	var b bytes.Buffer
	n := printCard(&b, []card.ReaderReport{
		{Name: "内蔵リーダー", Internal: true, Card: "B-CAS", CASystemID: 0x0005, IDs: []string{"0000000000012345", "0000000000000001"}},
		{Name: "A-CAS 外付け", Card: "A-CAS", CASystemID: 0x0005},
		{Name: "塞がっている", Err: "共有違反"},
	})
	if n != 2 {
		t.Errorf("読めた数 = %d, want 2", n)
	}
	out := b.String()
	for _, want := range []string{
		"カードリーダー 3 個",
		"[内蔵] 内蔵リーダー", "B-CAS  CA_system_id 0x0005  カード ID 0000000000012345, 0000000000000001",
		"[外付け] A-CAS 外付け", "A-CAS  CA_system_id 0x0005  カード ID (IDI で返ってきませんでした)",
		"読めません: 共有違反",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("出力に %q がありません:\n%s", want, out)
		}
	}
}

func TestRun_devicesとcardは引数を足せない(t *testing.T) {
	for _, args := range [][]string{{"--devices", "x"}, {"--card", "zd1j:1-1"}} {
		if code := run(args); code != 2 {
			t.Errorf("%v = %d, want 2", args, code)
		}
	}
}
