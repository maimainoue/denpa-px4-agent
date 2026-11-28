package devices

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// px4_drv (fork) の macos/DriverHost_PX4.ini から抜いた節 (GUID は実物)。
const sampleHostIni = `[DriverHost_PX4]
PipeTimeout=5000

; コメント
[DeviceDefinition0]
Name="PLEX PX-W3U4"
GUID="{aa8de3fc-004d-49a4-b976-4250f15a976d}"
Type="PX4"
DeviceInterfaceGUID="{6d78c843-49d8-4bb8-8dfa-286fdee4c8a3}"

[DeviceDefinition0.Config]
VendorId=0x0511
ProductId=0x083F
XferPackets=816

[DeviceDefinition0.Receiver0]
Name="PLEX PX-W3U4 ISDB-S Receiver #0"
GUID="{641e1543-e26c-4305-9685-fee33882c1d5}"
System="ISDB-S"
Index=0

[DeviceDefinition2]
Name="PLEX PX-W3PE4"
GUID="{e231e39e-4823-41ff-ab71-f95bf6785ffc}"
Type="PX4"
DeviceInterfaceGUID="{47fc15e3-6791-4e24-8fbe-c4218eee0ad6}"

[DeviceDefinition2.Config]
VendorId=0x0511
ProductId=0x023F

[DeviceDefinition2.Receiver0]
System="ISDB-S"
Index=0

[DeviceDefinition17]
Name="e-Better DTV02A-5TS-P"
GUID="{dd587b1c-eda9-4092-b171-35e34b2d0c10}"
Type="PXMLT"
DeviceInterfaceGUID="{eeb8e93b-1007-4e3e-8ce9-4f7ebdf3aba1}"

[DeviceDefinition17.Config]
VendorId=0x0511
ProductId=0x924E

[DeviceDefinition17.Receiver0]
System="ISDB-T,ISDB-S"
Index=0

[DeviceDefinition17.Receiver1]
System="ISDB-T,ISDB-S"
Index=1

[DeviceDefinition15]
Name="MyGica T230C Clone"
GUID="{7c2ab781-65d8-4a51-bc5e-3af117612f2b}"
Type="T230C"
DeviceInterfaceGUID="{1c51af64-942a-47f8-94ba-6534a2ba3c2f}"

[DeviceDefinition15.Receiver0]
System="ISDB-C"
Index=0

[DeviceDefinition30]
Name="Unused Model"
GUID="{00000000-0000-0000-0000-000000000030}"
Type="PX4"

[DeviceDefinition30.Config]
VendorId=0x0511
ProductId=0xffff

[DeviceDefinition30.Receiver0]
System="ISDB-T"
Index=0

[DeviceDefinition31]
Name="Card Only"
GUID="{00000000-0000-0000-0000-000000000031}"
Type="HZ2W"

[DeviceDefinition32]
Name="Weird"
GUID="{00000000-0000-0000-0000-000000000032}"

[DeviceDefinition32.Receiver0]
System="DVB-S2"
Index=0
`

func TestParseHostIni_節と値を読む(t *testing.T) {
	got := ParseHostIni([]byte("\xef\xbb\xbf"+strings.ReplaceAll(sampleHostIni, "\n", "\r\n")), nil)
	if len(got) != 7 {
		t.Fatalf("機種は 7 つのはず: %d", len(got))
	}
	d := got[0]
	if d.Name != "PLEX PX-W3U4" || d.GUID != "{aa8de3fc-004d-49a4-b976-4250f15a976d}" ||
		d.InterfaceGUID != "{6d78c843-49d8-4bb8-8dfa-286fdee4c8a3}" || d.VendorID != 0x0511 || d.ProductID != 0x083f {
		t.Errorf("W3U4: %+v", d)
	}
	if len(d.Receivers) != 1 || d.Receivers[0].System != "ISDB-S" {
		t.Errorf("受信機: %+v", d.Receivers)
	}
	if c := got[2]; c.VendorID != -1 || c.ProductID != -1 { // DeviceDefinition15 は Config が無い
		t.Errorf("Config が無ければ -1: %+v", c)
	}
}

func TestOverlayHostIni_既存の機種と新しい機種(t *testing.T) {
	defs := Builtin()
	w3u4Before, _ := defs.Get("PLEX PX-W3U4")
	recvBefore := len(w3u4Before.Receivers)
	var logs []string
	logf := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	defs.OverlayHostIni(ParseHostIni([]byte(sampleHostIni), logf), logf)

	// 同じ GUID: 受信機・Denpa の印は変わらない
	w3u4, _ := defs.Get("PLEX PX-W3U4")
	if len(w3u4.Receivers) != recvBefore || !w3u4.Denpa || len(w3u4.USB) != 1 {
		t.Errorf("W3U4 は変わらないはず: %+v", w3u4)
	}
	// 同じ名前で GUID が無い: GUID を足す。受信機はそのまま
	w3pe4, _ := defs.Get("PLEX PX-W3PE4")
	if w3pe4.Host == nil || w3pe4.Host.DeviceGUID != "{e231e39e-4823-41ff-ab71-f95bf6785ffc}" ||
		w3pe4.Host.InterfaceGUID != "{47fc15e3-6791-4e24-8fbe-c4218eee0ad6}" || len(w3pe4.Receivers) != 4 {
		t.Errorf("W3PE4 に GUID が付くはず: %+v", w3pe4)
	}
	// 名前は違うが USB の ID が同じ機材は、組み込みの機種に付く (新しい機種は増えない)
	mlt5, _ := defs.Get("PLEX PX-MLT5PE")
	if _, ok := defs.Get("e-Better DTV02A-5TS-P"); ok || mlt5.Host == nil || mlt5.Host.DeviceGUID != "{dd587b1c-eda9-4092-b171-35e34b2d0c10}" || len(mlt5.Receivers) != 5 {
		t.Errorf("DTV02A-5TS-P は PX-MLT5PE に付くはず: %+v", mlt5)
	}
	// 新しい機種: 方式から types を決める。Config が無ければ USB を空のまま足す。ISDB-C は CATV
	cl, ok := defs.Get("MyGica T230C Clone")
	if !ok || len(cl.USB) != 0 || len(cl.Receivers) != 1 || strings.Join(cl.Receivers[0].Types, ",") != "CATV" || cl.Denpa {
		t.Errorf("Config が無い機種: %+v", cl)
	}
	// 無効・受信機なし・読めない方式は足さない
	for _, m := range []string{"Unused Model", "Card Only", "Weird"} {
		if _, ok := defs.Get(m); ok {
			t.Errorf("%s は足さないはず", m)
		}
	}
	if !strings.Contains(strings.Join(logs, "\n"), "ProductId=0xffff") {
		t.Errorf("無効の機種は記録に出るはず: %v", logs)
	}
}

func TestOverlayHostIni_同じ名前で別のGUIDは足さない(t *testing.T) {
	defs := Builtin()
	ini := "[DeviceDefinition0]\nName=\"PLEX PX-W3U4\"\nGUID=\"{11111111-1111-1111-1111-111111111111}\"\n\n[DeviceDefinition0.Receiver0]\nSystem=\"ISDB-T\"\nIndex=0\n"
	defs.OverlayHostIni(ParseHostIni([]byte(ini), nil), nil)
	d, _ := defs.Get("PLEX PX-W3U4")
	if !strings.EqualFold(d.Host.DeviceGUID, "{aa8de3fc-004d-49a4-b976-4250f15a976d}") {
		t.Errorf("元の GUID のまま: %+v", d.Host)
	}
}

func TestLoadHostIni_ファイル(t *testing.T) {
	if got := LoadHostIni(filepath.Join(t.TempDir(), "nothing.ini"), nil); got != nil {
		t.Errorf("無いファイルは何も返さない: %v", got)
	}
	p := filepath.Join(t.TempDir(), "DriverHost_PX4.ini")
	os.WriteFile(p, []byte(sampleHostIni), 0o644)
	if got := LoadHostIni(p, nil); len(got) != 7 {
		t.Errorf("機種は 7 つ: %d", len(got))
	}
	os.WriteFile(p, []byte{0xff, 0xfe, '[', 0}, 0o644)
	var logs []string
	if got := LoadHostIni(p, func(f string, a ...any) { logs = append(logs, f) }); got != nil || len(logs) != 1 {
		t.Errorf("UTF-16 は読まず、記録に出す: %v %v", got, logs)
	}
}

func iniOf(name, guid, usb string) string {
	cfg := ""
	if usb != "" {
		cfg = "[DeviceDefinition0.Config]\nVendorId=0x" + usb[:4] + "\nProductId=0x" + usb[5:] + "\n\n"
	}
	return "[DeviceDefinition0]\nName=\"" + name + "\"\nGUID=\"" + guid + "\"\n\n" + cfg +
		"[DeviceDefinition0.Receiver0]\nSystem=\"ISDB-T\"\nIndex=0\n"
}

func TestOverlayHostIni_名前が違う同じ機材は組み込みの機種に付く(t *testing.T) {
	cases := []struct{ name, usb, want string }{
		{"Digibest ISDB2056", "", "DTV02-1T1S-U"},                // 別名 (Windows の ini。USB の ID が無い)
		{"Digibest ISDB2056N", "0511:084b", "DTV02A-1T1S-U"},     // 別名と USB の両方
		{"Unknown Name", "0511:0052", "e-Better DTV03A-1TU"},     // USB の ID だけ
		{"e-Better DTV02A-5TS-P", "0511:924e", "PLEX PX-MLT5PE"}, // 3 つの ID を持つ機種の 1 つ
	}
	for _, c := range cases {
		defs := Builtin()
		n := len(defs.All())
		guid := "{aaaaaaaa-0000-0000-0000-000000000001}"
		defs.OverlayHostIni(ParseHostIni([]byte(iniOf(c.name, guid, c.usb)), nil), nil)
		if len(defs.All()) != n {
			t.Errorf("%s: 機種が増えたはずがない (%d → %d)", c.name, n, len(defs.All()))
		}
		d, _ := defs.Get(c.want)
		if d.Host == nil || d.Host.DeviceGUID != guid {
			t.Errorf("%s: %s に GUID が付くはず: %+v", c.name, c.want, d.Host)
		}
	}
}

func TestOverlayHostIni_USBのIDが複数の機種に当たるときは決めない(t *testing.T) {
	defs := Builtin()
	defs.Put(Def{Model: "Twin", USB: []string{"0511:924e"}}) // PX-MLT5PE と同じ ID
	n := len(defs.All())
	defs.OverlayHostIni(ParseHostIni([]byte(iniOf("Other", "{bbbbbbbb-0000-0000-0000-000000000002}", "0511:924e")), nil), nil)
	if len(defs.All()) != n+1 {
		t.Errorf("決められないときは、新しい機種として足すはず (%d → %d)", n, len(defs.All()))
	}
}
