//go:build !windows

package chardev

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/devices"
)

type fakeSys struct {
	t   *testing.T
	dev string
	sys string
}

func newFakeSys(t *testing.T) *fakeSys {
	root := t.TempDir()
	f := &fakeSys{t: t, dev: filepath.Join(root, "dev"), sys: filepath.Join(root, "sys")}
	os.MkdirAll(f.dev, 0o755)
	os.MkdirAll(f.sys, 0o755)
	return f
}

// usb は USB デバイス (バス-ポート) を作る。
func (f *fakeSys) usb(bus, path, vid, pid, serial string) string {
	dir := filepath.Join(f.sys, "devices", "usb"+bus, bus+"-"+path)
	os.MkdirAll(filepath.Join(dir, bus+"-"+path+":1.0"), 0o755)
	for k, v := range map[string]string{"idVendor": vid, "idProduct": pid, "serial": serial, "busnum": bus, "devpath": path} {
		if v != "" {
			os.WriteFile(filepath.Join(dir, k), []byte(v+"\n"), 0o644)
		}
	}
	return filepath.Join(dir, bus+"-"+path+":1.0")
}

// node はデバイスファイルを作り、sysfs のクラスから USB インターフェースへのリンクを張る。
func (f *fakeSys) node(devname string, n int, iface string) {
	name := devname + string(rune('0'+n))
	os.WriteFile(filepath.Join(f.dev, name), nil, 0o644)
	if iface == "" {
		return
	}
	class := devname[:len(devname)-len("video")]
	dir := filepath.Join(f.sys, "class", class, name)
	os.MkdirAll(dir, 0o755)
	if err := os.Symlink(iface, filepath.Join(dir, "device")); err != nil {
		f.t.Fatal(err)
	}
}

func names(specs []backend.TunerSpec) []string {
	var out []string
	for _, s := range specs {
		out = append(out, s.Name+" "+s.Device)
	}
	return out
}

func TestDetect_PX_W3U4が2台(t *testing.T) {
	f := newFakeSys(t)
	a := f.usb("1", "2", "0511", "083f", "000000000001")
	b := f.usb("1", "3", "0511", "083f", "000000000002")
	for i := 0; i < 4; i++ {
		f.node("px4video", i, a)
		f.node("px4video", 4+i, b)
	}
	got, err := Detect(allDefs(), f.dev, f.sys)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 8 {
		t.Fatalf("8 台のはず: %v", names(got))
	}
	if got[2].Name != "PLEX PX-W3U4 1-2 #2" || got[2].Device != "px4:"+"/dev/px4video2" {
		t.Errorf("仕様書の例: %+v", got[2])
	}
	if got[4].Name != "PLEX PX-W3U4 1-3 #0" {
		t.Errorf("2 台目: %+v", got[4])
	}
	if !reflect.DeepEqual(got[0].Types, []string{"BS", "CS"}) || !reflect.DeepEqual(got[3].Types, []string{"GR"}) {
		t.Errorf("types: %v %v", got[0].Types, got[3].Types)
	}
}

func TestDetect_PX_Q3U4はシリアルの末尾で筐体にまとめる(t *testing.T) {
	f := newFakeSys(t)
	a := f.usb("1", "4", "0511", "084a", "ABCDEFGHIJKLMN1")
	b := f.usb("1", "5", "0511", "084a", "ABCDEFGHIJKLMN2")
	for i := 0; i < 4; i++ {
		f.node("px4video", i, a)
		f.node("px4video", 4+i, b)
	}
	got, _ := Detect(allDefs(), f.dev, f.sys)
	if len(got) != 8 {
		t.Fatalf("%v", names(got))
	}
	if got[0].Name != "PLEX PX-Q3U4 1-4 #0" || got[7].Name != "PLEX PX-Q3U4 1-4 #7" {
		t.Errorf("末尾 1 の側が #0〜#3、2 の側が #4〜#7: %v", names(got))
	}
	if got[7].Device != "px4:/dev/px4video7" || !reflect.DeepEqual(got[7].Types, []string{"GR"}) {
		t.Errorf("%+v", got[7])
	}
}

func TestDetect_片方しか無ければその側だけ(t *testing.T) {
	f := newFakeSys(t)
	b := f.usb("1", "5", "0511", "084a", "ABCDEFGHIJKLMN2")
	for i := 0; i < 4; i++ {
		f.node("px4video", i, b)
	}
	got, _ := Detect(allDefs(), f.dev, f.sys)
	if len(got) != 4 || got[0].Name != "PLEX PX-Q3U4 1-5 #4" {
		t.Errorf("%v", names(got))
	}
}

func TestDetect_同じdevnameでもUSBのIDで機種を分ける(t *testing.T) {
	f := newFakeSys(t)
	f.node("px4video", 0, f.usb("1", "2", "0572", "c68a", "")) // MyGica T230C
	f.node("px4video", 1, f.usb("1", "3", "3275", "0080", "")) // Siano RIO
	f.node("px4video", 2, f.usb("1", "4", "dead", "beef", "")) // 知らない機材
	got, _ := Detect(allDefs(), f.dev, f.sys)
	if len(got) != 2 || got[0].Name != "MyGica T230C 1-2 #0" || got[1].Name != "Siano RIO 1-3 #0" {
		t.Errorf("%v", names(got))
	}
	// T230C は CATV と SKY だけなので、自動検出の types (GR BS CS のうち) は空
	if len(got[0].Types) != 0 {
		t.Errorf("CATV・SKY は人が足す: %v", got[0].Types)
	}
}

func TestDetect_sysfsが読めなければdevnameが1機種に決まるときだけ(t *testing.T) {
	f := newFakeSys(t)
	f.node("pxmlt5video", 0, "")
	f.node("pxmlt5video", 1, "")
	f.node("px4video", 0, "") // 複数の機種があるので決められない
	got, _ := Detect(allDefs(), f.dev, f.sys)
	if len(got) != 2 || got[0].Name != "PLEX PX-MLT5PE #0" || got[1].Device != "px4:/dev/pxmlt5video1" {
		t.Errorf("%v", names(got))
	}
	if !reflect.DeepEqual(got[0].Types, []string{"GR", "BS", "CS"}) {
		t.Errorf("types: %v", got[0].Types)
	}
}

func TestDetect_typesの上書きが効く(t *testing.T) {
	f := newFakeSys(t)
	f.node("pxmlt5video", 0, f.usb("4", "12.1", "0511", "024e", ""))
	defs := allDefs()
	d, _ := defs.Get("PLEX PX-MLT5PE")
	d2 := *d
	d2.Receivers = []devices.Receiver{{Index: 0, Types: []string{"GR"}}}
	defs.Put(d2)
	got, _ := Detect(defs, f.dev, f.sys)
	if len(got) != 1 || got[0].Name != "PLEX PX-MLT5PE 4-12.1 #0" || !reflect.DeepEqual(got[0].Types, []string{"GR"}) {
		t.Errorf("%+v", got)
	}
}

// allDefs は、denpa が対応している機種も自動検出の対象にした機種表 (DETECT_INCLUDE=denpa)。
func allDefs() *devices.Defs {
	d := devices.Builtin()
	d.SetDetectInclude([]string{devices.IncludeAllDenpa})
	return d
}

func TestDetect_denpaが対応している機種は既定で外し_DETECT_INCLUDEで対象にする(t *testing.T) {
	f := newFakeSys(t)
	f.node("px4video", 0, f.usb("1", "2", "0511", "083f", ""))     // PX-W3U4 (denpa が対応)
	f.node("px4video", 1, f.usb("1", "3", "0572", "c68a", ""))     // MyGica T230C
	f.node("xzvideo", 0, f.usb("1", "4", "04bb", "0530", ""))      // GV-MVP/XZ
	f.node("tornevideo", 0, f.usb("1", "5", "054c", "04b2", ""))   // Sony CECH-ZD1 J (denpa が対応)

	defs := devices.Builtin()
	got, _ := Detect(defs, f.dev, f.sys)
	ns := names(got)
	if len(got) != 2 || got[0].Device != "px4:/dev/px4video1" || got[1].Device != "px4:/dev/xzvideo0" {
		t.Fatalf("T230C と XZ だけが並ぶはず: %v", ns)
	}
	sk := defs.Skipped()
	if len(sk) != 2 || sk[0].Model != "PLEX PX-W3U4" || sk[0].N != 1 || sk[1].Model != "Sony CECH-ZD1 J" {
		t.Errorf("対象外は PX-W3U4 と CECH-ZD1 J のはず: %v", sk)
	}

	// 機種名で選ぶ
	defs = devices.Builtin()
	defs.SetDetectInclude([]string{"Sony CECH-ZD1 J"})
	got, _ = Detect(defs, f.dev, f.sys)
	if len(got) != 3 {
		t.Errorf("CECH-ZD1 J が加わって 3 件のはず: %v", names(got))
	}
}
