package host

import (
	"errors"
	"strings"
	"testing"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/devices"
)

type fakeEnum struct {
	iface    map[string][]string // 小文字の GUID → デバイスパス
	usb      []string
	ifaceErr error
	usbErr   error
}

func (f fakeEnum) InterfacePaths(guid string) ([]string, error) {
	if f.ifaceErr != nil {
		return nil, f.ifaceErr
	}
	return f.iface[strings.ToLower(guid)], nil
}
func (f fakeEnum) USBDevices() ([]string, error) { return f.usb, f.usbErr }

func usbPath(vid, pid, serial string) string {
	return `\?\usb#vid_` + vid + `&pid_` + pid + `#` + serial + `#{a5dcbf10-6530-11d2-901f-00c04fb951ed}`
}

const (
	ifW3U4 = "{6d78c843-49d8-4bb8-8dfa-286fdee4c8a3}"
	ifXZ   = "{efd1a173-b5da-4bbc-8ac1-b2318799ec9e}"
)

func names(specs []string) string { return strings.Join(specs, "|") }

func TestDetect_WinUSBの機材をインターフェースGUIDで数える(t *testing.T) {
	en := fakeEnum{
		iface: map[string][]string{
			ifW3U4: {`\?\usb#vid_0511&pid_083f#a#` + ifW3U4, `\?\usb#vid_0511&pid_083f#b#` + ifW3U4}, // 2 台
			ifXZ:   {`\?\usb#vid_04bb&pid_0530#c#` + ifXZ},
		},
		usb: []string{usbPath("0511", "083f", "a"), usbPath("0511", "083f", "b"), usbPath("04bb", "0530", "c")},
	}
	var logs []string
	got, err := Detect(allDefs(), en, func(f string, a ...any) { logs = append(logs, f) })
	if err != nil {
		t.Fatal(err)
	}
	var ns []string
	for _, s := range got {
		ns = append(ns, s.Name+" "+s.Device)
	}
	// PX-W3U4 は 2 台 × 4 受信機、XZ は 1 台 × 1 受信機
	if len(got) != 9 {
		t.Fatalf("9 件のはず: %v", ns)
	}
	if got[0].Name != "PLEX PX-W3U4 #0" || got[0].Device != "px4host:{aa8de3fc-004d-49a4-b976-4250f15a976d}:0:0" || strings.Join(got[0].Types, ",") != "BS,CS" {
		t.Errorf("1 台目の 0 番: %+v", got[0])
	}
	if got[2].Name != "PLEX PX-W3U4 #2" || strings.Join(got[2].Types, ",") != "GR" {
		t.Errorf("1 台目の 2 番: %+v", got[2])
	}
	if got[4].Name != "PLEX PX-W3U4 (2) #0" || got[4].Device != "px4host:{aa8de3fc-004d-49a4-b976-4250f15a976d}:0:1" {
		t.Errorf("2 台目の 0 番: %+v", got[4])
	}
	if got[8].Name != "I-O DATA GV-MVP/XZ #0" || got[8].Device != "px4host:{9D54F32C-9BD4-4758-B0FA-3B8050CB29E0}:0:0" {
		t.Errorf("XZ: %+v", got[8])
	}
	if len(logs) != 0 {
		t.Errorf("警告は出ないはず: %v", logs)
	}
}

func TestDetect_ドライバーが入っていない機材は数えず警告する(t *testing.T) {
	en := fakeEnum{
		iface: map[string][]string{}, // WinUSB の機材は無い
		usb:   []string{usbPath("04bb", "0530", "c")},
	}
	var logs []string
	got, err := Detect(allDefs(), en, func(f string, a ...any) { logs = append(logs, strings.TrimSpace(f)) })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("ドライバーが無い機材は並べない: %+v", got)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "WinUSB") {
		t.Errorf("警告が 1 つのはず: %v", logs)
	}
}

func TestDetect_interfaceGuid_が無い機種はUSBのIDで数える(t *testing.T) {
	defs := devices.NewDefs()
	defs.Put(devices.Def{Model: "X", USB: []string{"1234:abcd"}, Host: &devices.Host{DeviceGUID: "{11111111-1111-1111-1111-111111111111}"},
		Receivers: []devices.Receiver{{Index: 0, Types: []string{"GR"}}}})
	en := fakeEnum{usb: []string{usbPath("1234", "ABCD", "1"), usbPath("1234", "abcd", "2"), usbPath("9999", "0000", "3")}}
	got, err := Detect(defs, en, nil)
	if err != nil || len(got) != 2 || got[1].Name != "X (2) #0" || got[1].Device != "px4host:{11111111-1111-1111-1111-111111111111}:0:1" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestDetect_受信機の無い機種は並べない(t *testing.T) {
	// HZ2W は内蔵カードだけ
	en := fakeEnum{
		iface: map[string][]string{"{cd6ae7c3-5a4d-42a4-b33f-f0f0a6cfaeed}": {`x`}},
		usb:   []string{usbPath("04bb", "0526", "1")},
	}
	got, err := Detect(allDefs(), en, nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestDetect_列挙に失敗したら_何も見つからないときだけエラー(t *testing.T) {
	_, err := Detect(allDefs(), fakeEnum{ifaceErr: errors.New("boom"), usbErr: errors.New("boom")}, nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	// USB の列挙だけ失敗しても、GUID で数えられる機種は並ぶ
	en := fakeEnum{iface: map[string][]string{ifXZ: {`x`}}, usbErr: errors.New("usb")}
	got, err := Detect(allDefs(), en, nil)
	if err != nil || len(got) != 1 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestDetect_macOSのようにインターフェースGUIDが無い_OSは_USBのIDだけで数える(t *testing.T) {
	// 組み込みの機種は interfaceGuid を持つが、InterfacePaths が ErrNoInterface を返すなら、警告なしで USB の数を使う
	en := fakeEnum{ifaceErr: ErrNoInterface, usb: []string{"iokit#vid_04bb&pid_0530#0", "iokit#vid_04bb&pid_0530#1"}}
	var logs []string
	got, err := Detect(allDefs(), en, func(f string, a ...any) { logs = append(logs, f) })
	if err != nil || len(got) != 2 || got[1].Name != "I-O DATA GV-MVP/XZ (2) #0" || got[1].Device != "px4host:{9D54F32C-9BD4-4758-B0FA-3B8050CB29E0}:0:1" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if len(logs) != 0 {
		t.Errorf("警告は出ないはず: %v", logs)
	}
}

func TestUsbID(t *testing.T) {
	for in, want := range map[string]string{
		usbPath("0511", "083F", "x"): "0511:083f",
		`\?\hid#foo`:                 "",
	} {
		if got := usbID(in); got != want {
			t.Errorf("%q: %q (want %q)", in, got, want)
		}
	}
}

// allDefs は、denpa が対応している機種も自動検出の対象にした機種表 (DETECT_INCLUDE=denpa)。
func allDefs() *devices.Defs {
	d := devices.Builtin()
	d.SetDetectInclude([]string{devices.IncludeAllDenpa})
	return d
}

func TestDetect_denpaが対応している機種は既定で外し_DETECT_INCLUDEで対象にする(t *testing.T) {
	en := fakeEnum{
		iface: map[string][]string{
			ifW3U4: {`\?\usb#vid_0511&pid_083f#a#` + ifW3U4, `\?\usb#vid_0511&pid_083f#b#` + ifW3U4},
			ifXZ:   {`\?\usb#vid_04bb&pid_0530#c#` + ifXZ},
		},
		usb: []string{usbPath("0511", "083f", "a"), usbPath("0511", "083f", "b"), usbPath("04bb", "0530", "c")},
	}
	defs := devices.Builtin()
	got, err := Detect(defs, en, nil)
	if err != nil || len(got) != 1 || got[0].Name != "I-O DATA GV-MVP/XZ #0" {
		t.Fatalf("PX-W3U4 は外れて XZ だけ並ぶはず: got=%+v err=%v", got, err)
	}
	if sk := defs.Skipped(); len(sk) != 1 || sk[0].Model != "PLEX PX-W3U4" || sk[0].N != 2 {
		t.Errorf("対象外は PX-W3U4 の 2 台のはず: %v", sk)
	}

	// 機種名で対象にする
	defs = devices.Builtin()
	defs.SetDetectInclude([]string{"PLEX PX-W3U4"})
	got, _ = Detect(defs, en, nil)
	if len(got) != 9 || len(defs.Skipped()) != 0 {
		t.Errorf("9 件 (W3U4 は 2 台 × 4 + XZ) のはず: %d 件、対象外 %v", len(got), defs.Skipped())
	}
}

func TestDetect_インターフェースの列挙だけが失敗したら_USBに見えても並べない(t *testing.T) {
	// WinUSB の有無が分からないので、USB の数で代わりにしない (ドライバーが無い機材を並べてしまう)
	en := fakeEnum{ifaceErr: errors.New("boom"), usb: []string{usbPath("04bb", "0530", "c")}}
	var logs []string
	got, err := Detect(allDefs(), en, func(f string, a ...any) { logs = append(logs, f) })
	if len(got) != 0 {
		t.Fatalf("並べないはず: %+v", got)
	}
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("エラーになるはず: %v (logs %v)", err, logs)
	}
}

func TestDetect_WinUSBが入っていない対象外の機材も_対象外に数える(t *testing.T) {
	// PX-W3U4 が USB には 1 台見えるが WinUSB が無い (InterfacePaths は空)。既定では対象外なので、並べず、警告も出さず、対象外に 1 台と数える
	en := fakeEnum{iface: map[string][]string{}, usb: []string{usbPath("0511", "083f", "a")}}
	defs := devices.Builtin()
	var logs []string
	got, err := Detect(defs, en, func(f string, a ...any) { logs = append(logs, f) })
	if err != nil || len(got) != 0 || len(logs) != 0 {
		t.Fatalf("got=%+v err=%v logs=%v", got, err, logs)
	}
	if sk := defs.Skipped(); len(sk) != 1 || sk[0].Model != "PLEX PX-W3U4" || sk[0].N != 1 {
		t.Errorf("対象外に 1 台のはず: %v", sk)
	}
}

func TestDetect_typesはGR_BS_CSだけ_CATVとSKYは付けない(t *testing.T) {
	defs := devices.NewDefs()
	defs.Put(devices.Def{Model: "A", USB: []string{"1234:0001"}, Host: &devices.Host{DeviceGUID: "{1}"},
		Receivers: []devices.Receiver{{Index: 0, Types: []string{"GR", "CATV"}}}})
	defs.Put(devices.Def{Model: "B", USB: []string{"1234:0002"}, Host: &devices.Host{DeviceGUID: "{2}"},
		Receivers: []devices.Receiver{{Index: 0, Types: []string{"CATV"}}}})
	en := fakeEnum{usb: []string{usbPath("1234", "0001", "x"), usbPath("1234", "0002", "y")}}
	got, err := Detect(defs, en, nil)
	if err != nil || len(got) != 2 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if strings.Join(got[0].Types, ",") != "GR" || got[1].Types == nil || len(got[1].Types) != 0 {
		t.Errorf("A は GR、B は空のはず: %#v / %#v", got[0].Types, got[1].Types)
	}
}
