package chardev

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/devices"
)

// DevicePrefix は tuners.json の device の接頭辞。
const DevicePrefix = "px4:"

var detectTypes = []string{"GR", "BS", "CS"}

// devfile は /dev の 1 つのデバイスファイル。
type devfile struct {
	name string // px4video0
	num  int
	def  *devices.Def
	usb  *usbDev // sysfs が読めたとき
}

type usbDev struct {
	key     string // バスとポート (`4-12.1`)
	vendor  string
	product string
	serial  string
}

// Detect は /dev から機材を見つけて並べる (4.2)。devDir は /dev、sysDir は /sys。
func Detect(defs *devices.Defs, devDir, sysDir string) ([]backend.TunerSpec, error) {
	entries, err := os.ReadDir(devDir)
	if err != nil {
		return nil, err
	}
	all := defs.All()
	var files []*devfile
	for _, e := range entries {
		for i := range all {
			d := &all[i]
			if d.Linux == nil || d.Linux.Devname == "" {
				continue
			}
			m := regexp.MustCompile("^" + regexp.QuoteMeta(d.Linux.Devname) + `(\d+)$`).FindStringSubmatch(e.Name())
			if m == nil {
				continue
			}
			n, _ := strconv.Atoi(m[1])
			files = append(files, &devfile{name: e.Name(), num: n})
			break
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if a, b := devname(files[i].name), devname(files[j].name); a != b {
			return a < b
		}
		return files[i].num < files[j].num
	})

	for _, f := range files {
		f.usb = readUSB(sysDir, f.name)
		f.def = pickDef(all, f)
	}

	type housing struct {
		model string
		def   *devices.Def
		port  string
		files []*devfile // 受信機の並び
	}
	var out []backend.TunerSpec
	var unknown []*devfile
	byKey := map[string]*housing{}
	var order []*housing
	for _, f := range files {
		if f.def == nil {
			unknown = append(unknown, f)
			continue
		}
		if f.usb == nil { // sysfs が読めない: 名前の数字のまま
			out = append(out, specFor(f.def, "", f.num, f, true))
			continue
		}
		key := f.usb.key
		if f.def.PairBySerial {
			key = "pair:" + f.def.Model + ":" + serialBase(f.usb.serial)
		}
		h := byKey[key]
		if h == nil {
			h = &housing{model: f.def.Model, def: f.def, port: f.usb.key}
			byKey[key] = h
			order = append(order, h)
		}
		h.files = append(h.files, f)
	}

	for _, h := range order {
		files := h.files
		if h.def.PairBySerial {
			// 末尾 1 の側を先に並べる。片方しか無ければ、その側だけ
			sort.SliceStable(files, func(i, j int) bool {
				return serialSide(files[i].usb.serial) < serialSide(files[j].usb.serial)
			})
			h.port = files[0].usb.key
			per := len(h.def.Receivers) // USB デバイス 1 つが受信機を全部持つ
			sides := map[int][]*devfile{}
			for _, f := range files {
				sides[serialSide(f.usb.serial)] = append(sides[serialSide(f.usb.serial)], f)
			}
			for _, side := range []int{1, 2} {
				for i, f := range sides[side] {
					out = append(out, specFor(h.def, h.port, (side-1)*per+i, f, false))
				}
			}
			continue
		}
		for i, f := range files {
			out = append(out, specFor(h.def, h.port, i, f, false))
		}
	}
	// 機種が決まらないものは、sysfs が読めない場合に 1 機種に決まるときだけ出している。ここでは出さない
	_ = unknown
	return out, nil
}

func devname(file string) string { return strings.TrimRight(file, "0123456789") }

func specFor(def *devices.Def, port string, idx int, f *devfile, fallback bool) backend.TunerSpec {
	name := fmt.Sprintf("%s #%d", def.Model, idx)
	if port != "" {
		name = fmt.Sprintf("%s %s #%d", def.Model, port, idx)
	}
	types := []string{}
	if len(def.Receivers) > 0 {
		for _, t := range detectTypes {
			for _, rt := range def.Receivers[idx%len(def.Receivers)].Types {
				if t == rt {
					types = append(types, t)
				}
			}
		}
	}
	return backend.TunerSpec{Name: name, Types: types, Device: DevicePrefix + "/dev/" + f.name}
}

// pickDef は devname と USB の ID から機種を決める。
func pickDef(all []devices.Def, f *devfile) *devices.Def {
	var cands []*devices.Def
	for i := range all {
		d := &all[i]
		if d.Linux != nil && devname(f.name) == d.Linux.Devname {
			cands = append(cands, d)
		}
	}
	if f.usb != nil {
		id := f.usb.vendor + ":" + f.usb.product
		for _, d := range cands {
			for _, u := range d.USB {
				if strings.EqualFold(u, id) {
					return d
				}
			}
		}
		return nil
	}
	if len(cands) == 1 { // devname が 1 機種に決まるときだけ
		return cands[0]
	}
	return nil
}

// readUSB は /sys/class/<クラス>/<名前>/device の先 (USB インターフェース) の親を読む。
func readUSB(sysDir, name string) *usbDev {
	class := strings.TrimSuffix(devname(name), "video")
	link := filepath.Join(sysDir, "class", class, name, "device")
	real, err := filepath.EvalSymlinks(link)
	if err != nil {
		return nil
	}
	dev := filepath.Dir(real) // インターフェースの親が USB デバイス
	read := func(f string) string {
		b, err := os.ReadFile(filepath.Join(dev, f))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	u := &usbDev{vendor: read("idVendor"), product: read("idProduct"), serial: read("serial")}
	if u.vendor == "" || u.product == "" {
		return nil
	}
	if bus, path := read("busnum"), read("devpath"); bus != "" && path != "" {
		u.key = bus + "-" + path
	} else {
		u.key = filepath.Base(dev)
	}
	return u
}

func serialBase(s string) string {
	if len(s) > 14 {
		return s[:14]
	}
	return s
}

// serialSide は PX-Q3 系の USB のシリアル番号の末尾 (1 か 2)。
func serialSide(s string) int {
	if strings.HasSuffix(s, "2") {
		return 2
	}
	return 1
}
