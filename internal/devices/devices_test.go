package devices

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "devices.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

type logs []string

func (l *logs) add(format string, args ...any) {
	*l = append(*l, strings.TrimSpace(fmt.Sprintf(format, args...)))
}

func (l logs) has(sub string) bool {
	for _, s := range l {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestLoadFile_書き損じはその項目だけ飛ばす(t *testing.T) {
	p := writeFile(t, `{"devices": [
	  {"model": "recpt1 (PT3)", "command": ["recpt1", "{channel}", "-", "-"], "channelFormat": "recpt1",
	   "receivers": [{"index": 0, "types": ["GR", "BS", "CS"]}]},
	  {"model": "format なし", "command": ["x"], "receivers": [{"index": 0, "types": ["GR"]}]},
	  {"model": "format 違い", "command": ["x"], "channelFormat": "foo", "receivers": [{"index": 0, "types": ["GR"]}]},
	  {"model": "受信機が 2 つ", "command": ["x"], "channelFormat": "denpa",
	   "receivers": [{"index": 0, "types": ["GR"]}, {"index": 1, "types": ["GR"]}]},
	  {"model": "種別違い", "command": ["x"], "channelFormat": "denpa", "receivers": [{"index": 0, "types": ["FM"]}]},
	  {"model": "px4rec の埋め込み", "command": ["x", "--ch={channel}"], "channelFormat": "px4rec",
	   "receivers": [{"index": 0, "types": ["GR"]}]},
	  {"model": "型が違う", "command": "recpt1"},
	  {"command": ["x"]},
	  {"model": "PLEX PX-W3U4", "usb": ["0511:083f"], "linux": {"devname": "px4video"},
	   "receivers": [{"index": 0, "types": ["BS"]}], "lnb": [15]}
	]}`)
	var l logs
	got := LoadFile(p, l.add)
	if len(got) != 2 || got[0].Model != "recpt1 (PT3)" || got[1].Model != "PLEX PX-W3U4" {
		t.Fatalf("読めた項目: %+v", got)
	}
	if len(l) != 7 {
		t.Errorf("飛ばした理由の行が 7 つのはず: %q", l)
	}
	for _, sub := range []string{"format 違い", "受信機が 2 つ", "種別違い", "px4rec の埋め込み", "7 番目を読めません", "8 番目"} {
		if !l.has(sub) {
			t.Errorf("%q の理由が出ていない: %q", sub, l)
		}
	}
	if !l.has("format なし: channelFormat がありません") {
		t.Errorf("理由が出ていない: %q", l)
	}
}

func TestLoadFile_ファイルが無いか壊れている(t *testing.T) {
	var l logs
	if got := LoadFile(filepath.Join(t.TempDir(), "nothing.json"), l.add); got != nil || len(l) != 0 {
		t.Errorf("無いファイルは黙って何も返さない: %v %q", got, l)
	}
	if got := LoadFile(writeFile(t, `{broken`), l.add); got != nil || len(l) != 1 {
		t.Errorf("壊れたファイルは記録に出して無いものとする: %v %q", got, l)
	}
	if got := LoadFile("", l.add); got != nil {
		t.Errorf("パスが空: %v", got)
	}
}

func TestDefs_modelをキーに重ねる(t *testing.T) {
	defs := NewDefs()
	defs.Put(Fake("/fake"))
	defs.Put(Def{Model: "A", Receivers: []Receiver{{Types: []string{"GR"}}}})
	var l logs
	defs.Overlay([]Def{
		{Model: "A", Receivers: []Receiver{{Types: []string{"GR", "BS"}}}},
		{Model: "B"},
	}, l.add)

	all := defs.All()
	if len(all) != 3 || all[0].Model != "fake" || all[1].Model != "A" || all[2].Model != "B" {
		t.Fatalf("順が崩れた: %+v", all)
	}
	a, _ := defs.Get("A")
	if !a.CanReceive("BS") {
		t.Errorf("types の上書きが効いていない: %+v", a.Receivable())
	}
	if !l.has("上書きしました: A") || !l.has("足しました: B") {
		t.Errorf("記録: %q", l)
	}
}

func TestDef_ReceivableとCanReceive(t *testing.T) {
	d := Def{Receivers: []Receiver{{Types: []string{"GR", "BS"}}}}
	if !d.CanReceive("GR") || !d.CanReceive("BS") || d.CanReceive("CS") {
		t.Errorf("CanReceive: %v", d.Receivable())
	}
}

func TestDef_SupportsLnb(t *testing.T) {
	cases := []struct {
		name string
		lnb  []int
		v    int
		want bool
	}{
		{"書いていなければ制限しない", nil, 15, true},
		{"[] は給電できない", []int{}, 15, false},
		{"[] でも 0 は通る", []int{}, 0, true},
		{"[15] に 15", []int{15}, 15, true},
		{"[15] に 11", []int{15}, 11, false},
	}
	for _, c := range cases {
		d := Def{LNB: c.lnb}
		if got := d.SupportsLnb(c.v); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestLoadFile_lnbの空配列と無いのを区別する(t *testing.T) {
	p := writeFile(t, `{"devices": [
	  {"model": "A", "lnb": []},
	  {"model": "B"}
	]}`)
	got := LoadFile(p, nil)
	if len(got) != 2 || got[0].LNB == nil || got[1].LNB != nil {
		t.Fatalf("lnb: %+v", got)
	}
}

func TestLoadFile_busyExitCodeは0も書ける(t *testing.T) {
	p := writeFile(t, `{"devices": [{"model": "A", "command": ["x"], "channelFormat": "denpa",
	  "receivers": [{"index": 0, "types": ["GR"]}], "busyExitCode": 0}]}`)
	got := LoadFile(p, nil)
	if len(got) != 1 || got[0].BusyExitCode == nil || *got[0].BusyExitCode != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestBuiltin_nasneはHTTPで呼ぶ(t *testing.T) {
	d, ok := Builtin().Get("Sony nasne (nasne-recpt1 HTTP)")
	if !ok {
		t.Fatal("組み込みに nasne が無い")
	}
	if !d.IsCmd() || d.ChannelFormat != FormatRecpt1 || !d.SingleService || !d.DecryptedByDevice {
		t.Errorf("nasne の定義: %+v", d)
	}
	if len(d.Command) != 0 {
		t.Errorf("curl は使わない (command は空): %v", d.Command)
	}
	if want := "http://{device}:8301/tuner/{channel}{sidQuery}"; d.URL != want || !d.IsHTTP() {
		t.Errorf("url: %q (want %q)", d.URL, want)
	}
	if d.BusyExitCode != nil {
		t.Errorf("busyExitCode は url では使わない: %v", *d.BusyExitCode)
	}
	if err := d.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	if got := d.Receivable(); len(got) != 3 || got[0] != "GR" || got[1] != "BS" || got[2] != "CS" {
		t.Errorf("receivable: %v", got)
	}
	if d.LNB == nil || len(d.LNB) != 0 {
		t.Errorf("lnb: %v (空で給電できないはず)", d.LNB)
	}
	if d.SupportsLnb(15) {
		t.Error("15V は出せないはず")
	}
}

func TestBuiltin_nasneの生のTS(t *testing.T) {
	d, ok := Builtin().Get("Sony nasne (nasne-recpt1 HTTP raw)")
	if !ok {
		t.Fatal("組み込みに nasne (生の TS) が無い")
	}
	if want := "http://{device}:8301/tuner/{channel}?bcas=0"; d.URL != want || !d.IsHTTP() {
		t.Errorf("url: %q (want %q)", d.URL, want)
	}
	// B-CAS を使わない生の TS: 復号はエージェントが行い、全部の局を含むので 1 サービスの制限もない
	if d.DecryptedByDevice || d.SingleService {
		t.Errorf("decryptedByDevice=%v singleService=%v (どちらも false のはず)", d.DecryptedByDevice, d.SingleService)
	}
	if err := d.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	if got := d.Receivable(); len(got) != 3 || got[0] != "GR" || got[1] != "BS" || got[2] != "CS" {
		t.Errorf("receivable: %v", got)
	}
	// 同じ機種名の前方一致で、B-CAS ありの機種と取り違えない
	if d2, _ := Builtin().Get("Sony nasne (nasne-recpt1 HTTP)"); d2 == nil || d2.URL == d.URL {
		t.Error("B-CAS ありの nasne の定義と区別できない")
	}
}

func TestFake_FAKE_TUNEは引数つきのコマンドラインも受ける(t *testing.T) {
	// 適合テストは `bun <スクリプト>` を渡す。空白で分けて、種別とチャンネル名を足す
	got := Fake("bun /x/tune.ts").Command
	want := []string{"bun", "/x/tune.ts", "{type}", "{channel}"}
	if len(got) != len(want) {
		t.Fatalf("Command = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Command[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// 1 語ならそのまま
	if got := Fake("/fake").Command; len(got) != 3 || got[0] != "/fake" {
		t.Errorf("Command = %q", got)
	}

	// 値そのものが存在するファイルなら (空白を含むパス)、分けない
	dir := t.TempDir() + "/with space"
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := dir + "/tune"
	if err := os.WriteFile(exe, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Fake(exe).Command; len(got) != 3 || got[0] != exe {
		t.Errorf("空白を含むパスを分けました: %q", got)
	}
}

func TestBuiltin_SonyCECHZD1Jは_DriverHost_PX4_のGUIDを持つ(t *testing.T) {
	d, ok := Builtin().Get("Sony CECH-ZD1 J")
	if !ok {
		t.Fatal("機種が無い")
	}
	// px4_drv の DriverHost_PX4.ini の [DeviceDefinition17] の GUID
	if d.Host == nil || d.Host.DeviceGUID != "{D3C0AEC3-0FC9-4769-8513-8323CEFD792F}" {
		t.Fatalf("host = %+v", d.Host)
	}
	if len(d.Receivers) != 1 || d.Receivers[0].Index != 0 || len(d.Receivers[0].Types) != 1 || d.Receivers[0].Types[0] != "GR" {
		t.Errorf("受信機は地上波 1 基のはず: %+v", d.Receivers)
	}
	if !d.CardReader {
		t.Error("内蔵のカードリーダーがあるはず")
	}
}

func TestModelOf_deviceから機種を引く(t *testing.T) {
	defs := Builtin()
	defs.Put(Def{Model: "試験の cmd 機", Command: []string{"/bin/true", "{channel}"}, ChannelFormat: FormatDenpa, Receivers: recv(rx("GR"))})
	defs.Put(Def{Model: "試験の cmd 機:長い", Command: []string{"/bin/true", "{channel}"}, ChannelFormat: FormatDenpa, Receivers: recv(rx("GR"))})

	for _, c := range []struct {
		device   string
		models   []string
		exact    bool
		receiver int
	}{
		{"px4host:{aa8de3fc-004d-49a4-b976-4250f15a976d}:2:0", []string{"PLEX PX-W3U4"}, true, 2},
		{"px4host:{AA8DE3FC-004D-49A4-B976-4250F15A976D}:0:1", []string{"PLEX PX-W3U4"}, true, 0}, // 大文字小文字・波かっこの有無を問わない
		{"px4host:aa8de3fc-004d-49a4-b976-4250f15a976d:3:0", []string{"PLEX PX-W3U4"}, true, 3},
		{"px4host:{D3C0AEC3-0FC9-4769-8513-8323CEFD792F}:0:0", []string{"Sony CECH-ZD1 J"}, true, 0},
		{"px4:/dev/pxmlt5video0", []string{"PLEX PX-MLT5PE"}, true, -1},
		{"cmd:試験の cmd 機:/dev/x", []string{"試験の cmd 機"}, true, -1},
		{"cmd:試験の cmd 機:長い:/dev/x", []string{"試験の cmd 機:長い"}, true, -1}, // 前方一致のうちいちばん長い機種
		{"cmd:Sony nasne (nasne-recpt1 HTTP):192.168.0.5", []string{"Sony nasne (nasne-recpt1 HTTP)"}, true, -1},
		{"", []string{ModelFake}, true, -1},
	} {
		r := defs.ModelOf(c.device)
		if len(r.Models) != len(c.models) || (len(c.models) > 0 && r.Models[0] != c.models[0]) || r.Exact != c.exact || r.Receiver != c.receiver {
			t.Errorf("%q: %+v, want models=%v exact=%v receiver=%d", c.device, r, c.models, c.exact, c.receiver)
		}
	}

	// px4: は devname を複数の機種が共有する (px4video)。1 つに決めず、候補を全部返す
	r := defs.ModelOf("px4:/dev/px4video2")
	if r.Exact || len(r.Models) < 2 || r.Models[0] != "PLEX PX-W3U4" {
		t.Errorf("候補になるはず: %+v", r)
	}

	// 引けないもの: 理由を持つ
	for _, device := range []string{
		"px4host:{00000000-0000-0000-0000-000000000000}:0:0",
		"cmd:無い機種:/dev/x",
		"px4:/dev/nothing0",
		"/dev/dvb/adapter0/frontend0",
	} {
		if r := defs.ModelOf(device); len(r.Models) != 0 || r.Note == "" || r.Exact {
			t.Errorf("%q: 引けないはず: %+v", device, r)
		}
	}
}

func TestBuiltin_GVMVPXZは_DriverHost_PX4_のGUIDを持つ(t *testing.T) {
	d, ok := Builtin().Get("I-O DATA GV-MVP/XZ")
	if !ok {
		t.Fatal("機種が無い")
	}
	// px4_drv の DriverHost_PX4.ini の [DeviceDefinition21] の GUID
	if d.Host == nil || d.Host.DeviceGUID != "{9D54F32C-9BD4-4758-B0FA-3B8050CB29E0}" {
		t.Fatalf("host = %+v", d.Host)
	}
	if len(d.Receivers) != 1 || d.Receivers[0].Types[0] != "GR" {
		t.Errorf("受信機は地上波 1 基のはず: %+v", d.Receivers)
	}
	if r := Builtin().ModelOf("px4host:{9d54f32c-9bd4-4758-b0fa-3b8050cb29e0}:0:0"); !r.Exact || r.Models[0] != "I-O DATA GV-MVP/XZ" {
		t.Errorf("GUID から引けない: %+v", r)
	}
}
