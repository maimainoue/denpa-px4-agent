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
	wantCmd := []string{"curl", "-sfN", "http://{device}:8301/tuner/{channel}{sidQuery}"}
	if len(d.Command) != len(wantCmd) {
		t.Fatalf("command: %v", d.Command)
	}
	for i := range wantCmd {
		if d.Command[i] != wantCmd[i] {
			t.Errorf("command[%d]: %q (want %q)", i, d.Command[i], wantCmd[i])
		}
	}
	if d.BusyExitCode == nil || *d.BusyExitCode != 22 {
		t.Errorf("busyExitCode: %v (want 22)", d.BusyExitCode)
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
