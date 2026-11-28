package host

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/devices"
)

// ErrNoInterface は、この OS にはデバイスインターフェース GUID で数える手段が無いこと (macOS)。
// InterfacePaths がこれを返したら、Detect は USB の ID だけで数える。
var ErrNoInterface = errors.New("この OS では、デバイスインターフェースの GUID で数えられません")

// Enumerator は機材の列挙 (Windows は SetupAPI、macOS は IOKit)。テストでは模擬に差し替える。
type Enumerator interface {
	// InterfacePaths は、デバイスインターフェース GUID (DriverHost_PX4.ini の DeviceInterfaceGUID) で、
	// いま挿さっている機材のデバイスパスを返す。WinUSB が入っている機材だけが現れる
	// (DriverHost_PX4 の `DeviceManager::Search` と同じ数え方)。
	InterfacePaths(guid string) ([]string, error)
	// USBDevices は挿さっている USB デバイスのデバイスパスを全部返す (`vid_xxxx&pid_xxxx` を含む)。ドライバーの有無は問わない。
	// macOS にはデバイスパスが無いので、`vid_xxxx&pid_xxxx` を含む文字列を作って返す。
	USBDevices() ([]string, error)
}

var usbIDRe = regexp.MustCompile(`(?i)vid_([0-9a-f]{4})&pid_([0-9a-f]{4})`)

// usbID はデバイスパスから `vvvv:pppp` (小文字) を取る。無ければ空。
func usbID(path string) string {
	m := usbIDRe.FindStringSubmatch(path)
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1] + ":" + m[2])
}

// Detect は挿さっている機材を、機種の定義 (host を持つもの) から数えて、受信機ごとのチューナーに並べる (仕様書 5.3)。
//   - host.interfaceGuid があれば、WinUSB の入っている機材をその GUID で数える (DriverHost_PX4 が開ける台数)
//   - USB の ID (`usb`) でも数える。ドライバーが無い機材は interfaceGuid の数に入らないので、差を log に出す。
//     interfaceGuid が無い機種は、USB の ID の数を使う
//   - 受信機の無い機種 (内蔵カードだけの機種) は並べない
//
// 名前は `<機種名> #<受信機>`、2 台目以降は `<機種名> (2) #<受信機>`。device は `px4host:<GUID>:<受信機の番号>:<台目 (0 から)>`。
// 同じ機種が複数台あるとき、どの筐体を開くかは DriverHost_PX4 が決める。
func Detect(defs *devices.Defs, en Enumerator, log backend.Logf) ([]backend.TunerSpec, error) {
	if log == nil {
		log = func(string, ...any) {}
	}
	var usb []string
	var usbErr error
	usbDone := false
	usbIDs := func() ([]string, error) { // 1 回だけ列挙する (USB を見る機種があるときだけ)
		if !usbDone {
			usbDone = true
			var paths []string
			paths, usbErr = en.USBDevices()
			for _, p := range paths {
				if id := usbID(p); id != "" {
					usb = append(usb, id)
				}
			}
		}
		return usb, usbErr
	}

	var out []backend.TunerSpec
	var errs []string
	for _, d := range defs.All() {
		if d.Host == nil || len(d.Receivers) == 0 {
			continue
		}
		n := 0
		viaIface := false
		if d.Host.InterfaceGUID != "" {
			paths, err := en.InterfacePaths(d.Host.InterfaceGUID)
			switch {
			case err == nil:
				n, viaIface = len(paths), true
			case !errors.Is(err, ErrNoInterface): // ErrNoInterface (macOS) だけが USB の ID で数える。ほかのエラーは WinUSB の有無が分からないので、並べない
				errs = append(errs, fmt.Sprintf("%s: %v", d.Model, err))
				continue
			}
		}
		nUSB := 0
		if len(d.USB) > 0 {
			ids, err := usbIDs()
			if err != nil && !viaIface {
				errs = append(errs, fmt.Sprintf("%s: %v", d.Model, err))
			}
			for _, id := range ids {
				for _, u := range d.USB {
					if strings.EqualFold(u, id) {
						nUSB++
						break
					}
				}
			}
		}
		warn := viaIface && nUSB > n
		if !viaIface {
			n = nUSB
		}
		// denpa が対応している機種は、DETECT_INCLUDE で選ばれない限り、自動検出の対象にしない
		if !defs.Detectable(&d) {
			// WinUSB が入っていない機材も、見えている以上は対象外として数える (警告は出さない。使わない機種なので)
			for k := 1; k <= max(n, nUSB); k++ {
				defs.SkipDetect(d.Model, fmt.Sprintf("%s:%d", d.Host.DeviceGUID, k))
			}
			continue
		}
		if warn {
			log("%s が %d 台見えますが、WinUSB ドライバーが入っているのは %d 台です (入っていない機材は使えません。ドライバーを入れ直してください)", d.Model, nUSB, n)
		}
		for k := 1; k <= n; k++ {
			for _, r := range d.Receivers {
				name := fmt.Sprintf("%s #%d", d.Model, r.Index)
				if k > 1 {
					name = fmt.Sprintf("%s (%d) #%d", d.Model, k, r.Index)
				}
				out = append(out, backend.TunerSpec{
					Name:   name,
					Types:  devices.DetectedTypes(r.Types),
					Device: fmt.Sprintf("%s%s:%d:%d", DevicePrefix, d.Host.DeviceGUID, r.Index, k-1),
				})
			}
		}
	}
	if len(errs) > 0 && len(out) == 0 {
		return nil, fmt.Errorf("機材を数えられません: %s", strings.Join(errs, "; "))
	}
	for _, e := range errs {
		log("機材を数えられません: %s", e)
	}
	return out, nil
}
