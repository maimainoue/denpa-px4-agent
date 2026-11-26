package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/config"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/logx"
)

func cliConfig(t *testing.T, dir string, extra map[string]string) *config.Config {
	t.Helper()
	env := map[string]string{"TUNERS_FILE": filepath.Join(dir, "tuners.json"), "DEVICES_FILE": filepath.Join(dir, "devices.json"), "DENPA_HOME": dir}
	for k, v := range extra {
		env[k] = v
	}
	cfg, err := config.Load(func(k string) string { return env[k] }, "linux", filepath.Join(dir, "agent"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestListDevices_定義とtuners_json(t *testing.T) {
	dir := t.TempDir()
	cfg := cliConfig(t, dir, nil)

	// tuners.json も devices.json も無い: 組み込みの定義だけ
	rep := ListDevices(cfg, logx.New(nil))
	if rep.Found || len(rep.Configured) != 0 {
		t.Errorf("tuners.json は無いはず: %+v", rep)
	}
	builtin := len(rep.Defs)
	if builtin == 0 {
		t.Fatal("組み込みの定義が無い")
	}
	if rep.TunersFile != cfg.TunersFile {
		t.Errorf("TunersFile = %q", rep.TunersFile)
	}

	// devices.json で機種を足し、tuners.json を置く
	os.WriteFile(filepath.Join(dir, "devices.json"), []byte(`{"devices":[{"model":"試験機","usb":["abcd:0001"],"receivers":[{"index":0,"types":["GR"]}]}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "tuners.json"), []byte(`{"tuners":[{"name":"t0","types":["GR"],"device":"px4host:{D3C0AEC3-0FC9-4769-8513-8323CEFD792F}:0:0"}]}`), 0o644)
	rep = ListDevices(cfg, logx.New(nil))
	if len(rep.Defs) != builtin+1 || rep.Defs[len(rep.Defs)-1].Model != "試験機" {
		t.Errorf("devices.json の機種が重ならない: %d 機種 (組み込みは %d)", len(rep.Defs), builtin)
	}
	if !rep.Found || len(rep.Configured) != 1 || rep.Configured[0].Name != "t0" {
		t.Errorf("tuners.json を読めない: %+v", rep)
	}
	// HasDetector は、この OS に検出を登録しているか (登録の有無はビルドする OS で決まる)
	if rep.HasDetector != (len(detectors) > 0) {
		t.Errorf("HasDetector = %v", rep.HasDetector)
	}
}
