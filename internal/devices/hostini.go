package devices

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DRIVER_HOST_INI の読み込み (仕様書 3.2・12.2)。
//
// px4_drv (fork) の DriverHost_PX4.ini から、機種の GUID・DeviceInterfaceGUID・USB の ID・受信機の方式を取り、
// 組み込みの機種表に重ねる。重ねる順は 組み込み → DRIVER_HOST_INI → devices.json。
//
// ini の形 (px4_drv の host/core/device_definition_set.cpp と同じ):
//
//	[DeviceDefinitionN]            Name・GUID・Type・DeviceInterfaceGUID
//	[DeviceDefinitionN.Config]     VendorId・ProductId (0x…。Windows の ini は一部の機種にしか無い)・転送の設定
//	[DeviceDefinitionN.ReceiverM]  Name・GUID・System ("ISDB-T"・"ISDB-S"・"ISDB-C"・"ISDB-T,ISDB-S")・Index
//
// N は Windows と macOS の ini で違うので、機種は GUID で引く。ini の Index は方式ごとの番号 (S0・S1・T0・T1) で、
// denpa の receivers[].index (筐体を通した番号) とは別物。足す機種の index には、受信機の節の番号 M を使う。

// HostIniDef は ini の `[DeviceDefinitionN]` 1 つ分。
type HostIniDef struct {
	Name          string
	GUID          string
	InterfaceGUID string
	VendorID      int // 無ければ -1
	ProductID     int // 無ければ -1
	Receivers     []HostIniReceiver
}

// HostIniReceiver は `[DeviceDefinitionN.ReceiverM]` 1 つ分。
type HostIniReceiver struct {
	Section int    // M
	System  string // "ISDB-T,ISDB-S" のような書き方のまま
}

// ini の ProductId をこの値にすると、host はその機種を使わない (px4_drv の docs の、使わない機種を外す書き方)。
const hostIniDisabledProductID = 0xffff

var hostIniSection = regexp.MustCompile(`^DeviceDefinition(\d+)(?:\.(Config)|\.Receiver(\d+))?$`)

// ParseHostIni は ini を読む。読めない行・節は記録に出して飛ばす。
func ParseHostIni(data []byte, logf func(format string, args ...any)) []HostIniDef {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))

	type recv struct {
		section int
		system  string
	}
	type def struct {
		HostIniDef
		recvs map[int]*recv
	}
	defs := map[int]*def{}
	get := func(n int) *def {
		d := defs[n]
		if d == nil {
			d = &def{HostIniDef: HostIniDef{VendorID: -1, ProductID: -1}, recvs: map[int]*recv{}}
			defs[n] = d
		}
		return d
	}

	var cur func(key, val string)
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == ';' || line[0] == '#' {
			continue
		}
		if line[0] == '[' {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				logf("DRIVER_HOST_INI の %d 行目を読めません (節の ] がありません)。この節は飛ばします", i+1)
				cur = nil
				continue
			}
			m := hostIniSection.FindStringSubmatch(strings.TrimSpace(line[1:end]))
			if m == nil {
				cur = nil // [DriverHost_PX4] など。機種の定義ではない
				continue
			}
			n, _ := strconv.Atoi(m[1])
			d := get(n)
			switch {
			case m[2] != "":
				cur = func(key, val string) {
					switch key {
					case "VendorId":
						d.VendorID = parseHexID(val)
					case "ProductId":
						d.ProductID = parseHexID(val)
					}
				}
			case m[3] != "":
				idx, _ := strconv.Atoi(m[3])
				r := &recv{section: idx}
				d.recvs[idx] = r
				cur = func(key, val string) {
					if key == "System" {
						r.system = val
					}
				}
			default:
				cur = func(key, val string) {
					switch key {
					case "Name":
						d.Name = val
					case "GUID":
						d.GUID = val
					case "DeviceInterfaceGUID":
						d.InterfaceGUID = val
					}
				}
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || cur == nil {
			continue
		}
		cur(strings.TrimSpace(k), unquote(strings.TrimSpace(v)))
	}

	nums := make([]int, 0, len(defs))
	for n := range defs {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	var out []HostIniDef
	for _, n := range nums {
		d := defs[n]
		if d.Name == "" || d.GUID == "" {
			logf("DRIVER_HOST_INI の [DeviceDefinition%d] に Name か GUID がありません。この機種は飛ばします", n)
			continue
		}
		ms := make([]int, 0, len(d.recvs))
		for m := range d.recvs {
			ms = append(ms, m)
		}
		sort.Ints(ms)
		for _, m := range ms {
			d.Receivers = append(d.Receivers, HostIniReceiver{Section: m, System: d.recvs[m].system})
		}
		out = append(out, d.HostIniDef)
	}
	return out
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// parseHexID は `0x0511` のような値を読む。読めなければ -1。
func parseHexID(s string) int {
	v, err := strconv.ParseUint(s, 0, 32)
	if err != nil {
		return -1
	}
	return int(v)
}

// LoadHostIni は DRIVER_HOST_INI を読む。ファイルが無ければ何も返さない (Linux など、使わない環境が普通)。
// 読めないファイルは記録に出して無いものとする。
func LoadHostIni(path string, logf func(format string, args ...any)) []HostIniDef {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logf("DRIVER_HOST_INI を読めません (%s): %v", path, err)
		}
		return nil
	}
	if bytes.HasPrefix(data, []byte{0xff, 0xfe}) || bytes.HasPrefix(data, []byte{0xfe, 0xff}) {
		logf("DRIVER_HOST_INI (%s) は UTF-16 です。UTF-8 にしてください。読みません", path)
		return nil
	}
	return ParseHostIni(data, logf)
}

// iniTypes は ini の System (カンマ区切り) を denpa の種別にする。読めない方式があれば ok=false。
func iniTypes(system string) (types []string, ok bool) {
	seen := map[string]bool{}
	add := func(ts ...string) {
		for _, t := range ts {
			if !seen[t] {
				seen[t] = true
				types = append(types, t)
			}
		}
	}
	for _, s := range strings.Split(system, ",") {
		switch strings.TrimSpace(s) {
		case "ISDB-T":
			add("GR")
		case "ISDB-S":
			add("BS", "CS")
		case "ISDB-C":
			add("CATV")
		default:
			return nil, false
		}
	}
	return types, len(types) > 0
}

func normGUID(g string) string { return strings.ToLower(strings.Trim(strings.TrimSpace(g), "{}")) }

func usbID(vid, pid int) string { return fmt.Sprintf("%04x:%04x", vid, pid) }

func hasUSB(list []string, id string) bool {
	for _, u := range list {
		if strings.EqualFold(u, id) {
			return true
		}
	}
	return false
}

// findByGUID は host.deviceGuid が同じ機種を引く。
func (s *Defs) findByGUID(guid string) *Def {
	g := normGUID(guid)
	for i := range s.list {
		if h := s.list[i].Host; h != nil && normGUID(h.DeviceGUID) == g {
			return &s.list[i]
		}
	}
	return nil
}

// OverlayHostIni は ini の機種を表に重ねる。
//
//   - 同じ GUID の機種がある: DeviceInterfaceGUID を ini の値にし、USB の ID が無ければ足す。受信機・Linux・Denpa の印は触らない。
//   - GUID は無いが、同じ名前で host の定義がまだ無い機種 (組み込みの PX-W3PE4 など) がある: GUID を足す。
//   - どちらも無い: 新しい機種として足す。受信機の方式から types を決める。受信機が無い・読めない方式がある機種は足さない。
//   - ProductId が 0xffff の機種は、host が使わないので飛ばす。
func (s *Defs) OverlayHostIni(items []HostIniDef, logf func(format string, args ...any)) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	for _, it := range items {
		if it.ProductID == hostIniDisabledProductID {
			logf("DRIVER_HOST_INI で %s は無効 (ProductId=0xffff) です。飛ばします", it.Name)
			continue
		}
		var usb string
		if it.VendorID >= 0 && it.ProductID >= 0 {
			usb = usbID(it.VendorID, it.ProductID)
		}

		d := s.findByGUID(it.GUID)
		if d == nil {
			var how string
			d, how = s.matchHostIni(it, usb)
			if d != nil && d.Host != nil && d.Host.DeviceGUID != "" {
				logf("DRIVER_HOST_INI の %s (%s) は、%s の %s が別の GUID (%s) で定義済みです。飛ばします", it.Name, it.GUID, how, d.Model, d.Host.DeviceGUID)
				continue
			}
			if d != nil && how != "同じ名前" {
				logf("DRIVER_HOST_INI の %s は、%s の %s として扱います", it.Name, how, d.Model)
			}
			if d != nil && len(d.Receivers) > 0 && len(d.Receivers) != len(it.Receivers) {
				logf("DRIVER_HOST_INI の %s は受信機が %d 個、%s は %d 個です。受信機は %s の定義のままです", it.Name, len(it.Receivers), d.Model, len(d.Receivers), d.Model)
			}
		}
		if d != nil {
			changed := false
			if d.Host == nil {
				d.Host = &Host{}
			}
			if d.Host.DeviceGUID == "" {
				d.Host.DeviceGUID = it.GUID
				changed = true
			}
			if it.InterfaceGUID != "" && !strings.EqualFold(d.Host.InterfaceGUID, it.InterfaceGUID) {
				d.Host.InterfaceGUID = it.InterfaceGUID
				changed = true
			}
			if usb != "" && !hasUSB(d.USB, usb) {
				d.USB = append(d.USB, usb)
				changed = true
			}
			if changed {
				logf("DRIVER_HOST_INI で機種を直しました: %s", d.Model)
			}
			continue
		}

		if len(it.Receivers) == 0 {
			logf("DRIVER_HOST_INI の %s は受信機の定義が無いので、機種として足しません (devices.json で足します)", it.Name)
			continue
		}
		nd := Def{Model: it.Name, Host: &Host{DeviceGUID: it.GUID, InterfaceGUID: it.InterfaceGUID}}
		if usb != "" {
			nd.USB = []string{usb}
		}
		bad := false
		for _, r := range it.Receivers {
			types, ok := iniTypes(r.System)
			if !ok {
				logf("DRIVER_HOST_INI の %s の受信機 %d の System (%q) を読めません。この機種は足しません", it.Name, r.Section, r.System)
				bad = true
				break
			}
			nd.Receivers = append(nd.Receivers, Receiver{Index: r.Section, Types: types})
		}
		if bad {
			continue
		}
		s.list = append(s.list, nd)
		logf("DRIVER_HOST_INI で機種を足しました: %s", nd.Model)
	}
	markDenpa(s)
}

// hostIniAlias は、ini の機種名が組み込みの機種名と違うものの対応 (小文字の ini の名前 → 組み込みの機種名)。
// Windows の ini は USB の ID が無い機種が多いので、USB の ID で突き合わせられない分を、名前で補う。
var hostIniAlias = map[string]string{
	"plex px-mlt8pe3":    "PLEX PX-MLT8PE (3)",
	"plex px-mlt8pe5":    "PLEX PX-MLT8PE",
	"digibest isdb6014":  "e-Better DTV02A-4TS-P",
	"digibest isdb2056":  "DTV02-1T1S-U",
	"digibest isdb2056n": "DTV02A-1T1S-U",
	"digibest isdbt2071": "e-Better DTV03A-1TU",
}

// matchHostIni は、GUID が同じ機種が無いときの、ini の機種と同じ機材の組み込みの機種を探す。
// 同じ名前 → 別名の表 → USB の ID (ちょうど 1 つの機種に当たるときだけ。共有する ID は決められない) の順。
func (s *Defs) matchHostIni(it HostIniDef, usb string) (d *Def, how string) {
	if d, ok := s.byName(it.Name); ok {
		return d, "同じ名前"
	}
	if m, ok := hostIniAlias[strings.ToLower(it.Name)]; ok {
		if d, ok := s.byName(m); ok {
			return d, "別名"
		}
	}
	if usb == "" {
		return nil, ""
	}
	var found *Def
	for i := range s.list {
		if hasUSB(s.list[i].USB, usb) {
			if found != nil {
				return nil, "" // 複数の機種が同じ ID を持つ。決められない
			}
			found = &s.list[i]
		}
	}
	if found != nil {
		return found, "USB の ID (" + usb + ")"
	}
	return nil, ""
}

func (s *Defs) byName(model string) (*Def, bool) {
	for i := range s.list {
		if strings.EqualFold(s.list[i].Model, model) {
			return &s.list[i], true
		}
	}
	return nil, false
}
