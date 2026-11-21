package cmdbackend

import (
	"testing"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/devices"
)

func cmdDef(model string, types ...string) devices.Def {
	return devices.Def{
		Model:         model,
		Command:       []string{"recpt1", "--device", "{device}", "{channel}", "-", "-"},
		ChannelFormat: devices.FormatRecpt1,
		Receivers:     []devices.Receiver{{Types: types}},
	}
}

func TestValidate_deviceの末尾はホスト名かIPv4(t *testing.T) {
	defs := devices.NewDefs()
	defs.Put(devices.Def{
		Model:         "Sony nasne (nasne-recpt1 HTTP)",
		Command:       []string{"curl", "-sfN", "http://{device}:8301/tuner/{channel}{sidQuery}"},
		ChannelFormat: devices.FormatRecpt1,
		Receivers:     []devices.Receiver{{Types: []string{"GR"}}},
	})
	defs.Put(cmdDef("recpt1 (PT3)", "GR")) // {device} だけの要素。検証しない
	b := New(Config{Defs: defs})

	for _, dev := range []string{"nasne.local", "192.168.0.43", "nasne"} {
		if err := b.Validate("cmd:Sony nasne (nasne-recpt1 HTTP):" + dev); err != nil {
			t.Errorf("%s: 通るはず: %v", dev, err)
		}
	}
	for _, dev := range []string{"127.0.0.1:8080/private#", "a@b", "a/b", "a b", "", "-a"} {
		if err := b.Validate("cmd:Sony nasne (nasne-recpt1 HTTP):" + dev); err == nil {
			t.Errorf("%q: 断るはず", dev)
		}
	}
	// {device} だけの要素なら、ホスト名でなくても通る
	if err := b.Validate("cmd:recpt1 (PT3):nasne.local:8301"); err != nil {
		t.Errorf("recpt1: 通るはず: %v", err)
	}
}

func TestOpen_deviceの末尾はホスト名かIPv4(t *testing.T) {
	defs := devices.NewDefs()
	defs.Put(devices.Def{
		Model:         "Sony nasne (nasne-recpt1 HTTP)",
		Command:       []string{"curl", "-sfN", "http://{device}:8301/tuner/{channel}{sidQuery}"},
		ChannelFormat: devices.FormatRecpt1,
		Receivers:     []devices.Receiver{{Types: []string{"GR"}}},
	})
	b := New(Config{Defs: defs})

	for _, dev := range []string{"127.0.0.1:8080/private#", "a@b", "a/b", "a b", "-a"} {
		_, err := b.Open(backend.TunerSpec{Name: "x", Device: "cmd:Sony nasne (nasne-recpt1 HTTP):" + dev})
		if err == nil || backend.StatusOf(err) != 400 {
			t.Errorf("%q: 400 のはず: %v", dev, err)
		}
	}
	if _, err := b.Open(backend.TunerSpec{Name: "x", Device: "cmd:Sony nasne (nasne-recpt1 HTTP):192.168.0.43"}); err != nil {
		t.Errorf("192.168.0.43: 通るはず: %v", err)
	}
}

func TestOpen_deviceから機種とデバイスを決める(t *testing.T) {
	defs := devices.NewDefs()
	defs.Put(cmdDef("recpt1 (PT3)", "GR"))
	defs.Put(cmdDef("Sony nasne (nasne-recpt1 HTTP)", "GR"))
	defs.Put(cmdDef("Sony nasne", "GR")) // 前方が同じ機種。長いほうが勝つ
	defs.Put(devices.Def{Model: "PLEX PX-W3U4", Receivers: []devices.Receiver{{Types: []string{"GR"}}}})
	b := New(Config{Defs: defs})

	cases := []struct {
		device    string
		wantModel string
		wantDev   string
	}{
		{"cmd:recpt1 (PT3):/dev/pt3video2", "recpt1 (PT3)", "/dev/pt3video2"},
		{"cmd:Sony nasne (nasne-recpt1 HTTP):nasne.local", "Sony nasne (nasne-recpt1 HTTP)", "nasne.local"},
		{"cmd:Sony nasne (nasne-recpt1 HTTP):nasne.local:8301", "Sony nasne (nasne-recpt1 HTTP)", "nasne.local:8301"},
		{"cmd:Sony nasne:nasne.local", "Sony nasne", "nasne.local"},
	}
	for _, c := range cases {
		tn, err := b.Open(backend.TunerSpec{Name: "x", Device: c.device})
		if err != nil {
			t.Errorf("%s: %v", c.device, err)
			continue
		}
		got := tn.(*tuner)
		if got.def.Model != c.wantModel || got.device != c.wantDev {
			t.Errorf("%s: model=%q device=%q", c.device, got.def.Model, got.device)
		}
	}
}

func TestOpen_断る(t *testing.T) {
	defs := devices.NewDefs()
	defs.Put(cmdDef("recpt1 (PT3)", "GR"))
	defs.Put(devices.Def{Model: "PLEX PX-W3U4", Receivers: []devices.Receiver{{Types: []string{"GR"}}}})
	b := New(Config{Defs: defs})

	for _, dev := range []string{
		"",                        // FAKE_TUNE が無ければ device は必須
		"px4:/dev/px4video0",      // ほかのバックエンド
		"cmd:unknown:/dev/x",      // 機種が無い
		"cmd:PLEX PX-W3U4:/dev/x", // command の無い機種
		"cmd:recpt1 (PT3)",        // ':' が無い
	} {
		_, err := b.Open(backend.TunerSpec{Name: "x", Device: dev})
		if err == nil || backend.StatusOf(err) != 400 {
			t.Errorf("%q: 400 のはず: %v", dev, err)
		}
	}
}

func TestFAKE_TUNE_deviceの無いチューナーを全部fakeとして扱う(t *testing.T) {
	b := New(Config{FakeTune: "/path/to/fake"})
	if !b.Handles(backend.TunerSpec{Device: ""}) || !b.Handles(backend.TunerSpec{Device: "cmd:x:y"}) {
		t.Error("Handles")
	}
	if New(Config{}).Handles(backend.TunerSpec{Device: ""}) {
		t.Error("FAKE_TUNE が無ければ device の無いものは扱わない")
	}
	tn, err := b.Open(backend.TunerSpec{Name: "fake0"})
	if err != nil {
		t.Fatal(err)
	}
	got := tn.(*tuner)
	if got.def.Model != "fake" {
		t.Fatalf("model=%q", got.def.Model)
	}
	args := BuildArgs(got.def, Params{Type: "BS", Channel: []string{"BS15:16625"}, TSID: 16625})
	if len(args) != 3 || args[0] != "/path/to/fake" || args[1] != "BS" || args[2] != "BS15:16625" {
		t.Errorf("fake の引数: %q", args)
	}
	// fake は全種別を受ける
	for _, typ := range []string{"GR", "BS", "CS", "CATV", "SKY"} {
		if !got.def.CanReceive(typ) {
			t.Errorf("fake は %s を受ける", typ)
		}
	}
}

func TestDetect_自動検出はしない(t *testing.T) {
	specs, err := New(Config{}).Detect(devices.NewDefs())
	if err != nil || specs != nil {
		t.Errorf("%v %v", specs, err)
	}
}

func TestDecryptedByDevice_SingleService(t *testing.T) {
	defs := devices.NewDefs()
	d := cmdDef("nasne", "GR", "BS")
	d.DecryptedByDevice, d.SingleService = true, true
	defs.Put(d)
	tn, err := New(Config{Defs: defs}).Open(backend.TunerSpec{Name: "n", Device: "cmd:nasne:nasne.local"})
	if err != nil {
		t.Fatal(err)
	}
	got := tn.(*tuner)
	if !got.DecryptedByDevice() || !got.SingleService() {
		t.Error("印が見えない")
	}
	if r := got.Receivable(); len(r) != 2 || r[0] != "GR" || r[1] != "BS" {
		t.Errorf("receivable: %v", r)
	}
}
