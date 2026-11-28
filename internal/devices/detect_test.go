package devices

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuiltin_denpaが対応している機種に印が付く(t *testing.T) {
	defs := Builtin()
	// denpa の It930x.cs・Siano.cs・WinUsbTargets.cs・Dt295.cs にある USB の ID の機種
	for _, m := range []string{
		"PLEX PX-W3U4", "PLEX PX-Q3PE5", "PLEX PX-MLT5PE", "PLEX PX-MLT8PE", "PLEX PX-MLT8PE (3)", "e-Better DTV02A-4TS-P",
		"DTV02-1T1S-U", "DTV02A-1T1S-U", "PLEX PX-M1UR", "PLEX PX-S1UR", "e-Better DTV03A-1TU",
		"Siano RIO", "Sony CECH-ZD1 J", "PIXELA PIX-DT295 Rev.01", "PIXELA PIX-DT295 Rev.02",
	} {
		if d, ok := defs.Get(m); !ok || !d.Denpa {
			t.Errorf("%s: denpa の印が付くはず (%+v)", m, d)
		}
	}
	// denpa が対応していない機種
	for _, m := range []string{
		"I-O DATA GV-MVP/XZ", "TurboSight TBS 5530", "MyGica T230C",
		"MyGica T230A", "I-O DATA GV-MVP/HZ2W", "Quixun QRS-UT100B", "Sony nasne (nasne-recpt1 HTTP)",
	} {
		if d, ok := defs.Get(m); !ok || d.Denpa {
			t.Errorf("%s: denpa の印は付かないはず (%+v)", m, d)
		}
	}
}

func TestDetectable_既定では_denpaが対応している機種を外す(t *testing.T) {
	defs := Builtin()
	w3u4, _ := defs.Get("PLEX PX-W3U4")
	xz, _ := defs.Get("I-O DATA GV-MVP/XZ")
	if defs.Detectable(w3u4) {
		t.Error("既定では PX-W3U4 は対象外のはず")
	}
	if !defs.Detectable(xz) {
		t.Error("denpa が対応していない機種は、いつも対象のはず")
	}
}

func TestDetectable_DETECT_INCLUDE_で機種名か_denpa_を選ぶ(t *testing.T) {
	defs := Builtin()
	w3u4, _ := defs.Get("PLEX PX-W3U4")
	q3u4, _ := defs.Get("PLEX PX-Q3U4")

	// 機種名 (大文字・小文字は区別しない)。書いた機種だけが対象になる
	if u := defs.SetDetectInclude([]string{" plex px-w3u4 ", ""}); len(u) != 0 {
		t.Fatalf("unknown = %v", u)
	}
	if !defs.Detectable(w3u4) || defs.Detectable(q3u4) {
		t.Errorf("PX-W3U4 だけが対象のはず: w3u4=%v q3u4=%v", defs.Detectable(w3u4), defs.Detectable(q3u4))
	}

	// denpa は、印の付いた機種を全部
	defs.SetDetectInclude([]string{"DENPA"})
	if !defs.Detectable(w3u4) || !defs.Detectable(q3u4) {
		t.Error("denpa なら全部対象のはず")
	}

	// 機種表に無い名前は、書き損じとして返す (対象にはならない)
	if u := defs.SetDetectInclude([]string{"PLEX PX-W3U5", "denpa"}); !reflect.DeepEqual(u, []string{"PLEX PX-W3U5"}) {
		t.Errorf("unknown = %v", u)
	}
}

func TestDetectable_devices_jsonで書いた機種は書いたとおり(t *testing.T) {
	defs := Builtin()
	// devices.json で同じ機種を書き直すと、denpa の印は書いたとおり (書かなければ偽) になる
	defs.Put(Def{Model: "PLEX PX-W3U4", USB: []string{"0511:083f"}})
	d, _ := defs.Get("PLEX PX-W3U4")
	if !defs.Detectable(d) {
		t.Error("denpa を書かなければ、対象のはず")
	}
	defs.Put(Def{Model: "PLEX PX-W3U4", USB: []string{"0511:083f"}, Denpa: true})
	d, _ = defs.Get("PLEX PX-W3U4")
	if defs.Detectable(d) {
		t.Error("denpa: true を書けば、対象外のはず")
	}
}

func TestSkipped_機種ごとに数えて_機種表の順に返す(t *testing.T) {
	defs := Builtin()
	defs.SkipDetect("Sony CECH-ZD1 J", "a")
	defs.SkipDetect("PLEX PX-W3U4", "x")
	defs.SkipDetect("PLEX PX-W3U4", "y")
	defs.SkipDetect("PLEX PX-W3U4", "y") // 同じ機材は 1 台
	got := defs.Skipped()
	want := []SkippedModel{{"PLEX PX-W3U4", 2}, {"Sony CECH-ZD1 J", 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%v", got)
	}
	if s := SkippedText(got); s != "PLEX PX-W3U4 ×2, Sony CECH-ZD1 J ×1" {
		t.Errorf("text = %q", s)
	}
	defs.ResetSkipped()
	if len(defs.Skipped()) != 0 || SkippedText(defs.Skipped()) != "" {
		t.Error("リセットすると空のはず")
	}
}

func TestDetectedTypes_CATVとSKYは付けない(t *testing.T) {
	for _, c := range []struct{ in, want []string }{
		{[]string{"GR", "CATV"}, []string{"GR"}},
		{[]string{"CATV", "SKY"}, []string{}},
		{[]string{"CS", "GR", "BS", "CATV", "SKY"}, []string{"GR", "BS", "CS"}},
		{nil, []string{}},
	} {
		got := DetectedTypes(c.in)
		if got == nil || strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%v: %#v (want %v)", c.in, got, c.want)
		}
	}
}
