// Package cmdbackend は recpt1 互換の CLI をチューナーとして使うバックエンド (仕様書 6 章)。
//
// 1 回の選局 = 子プロセス 1 つ。標準出力を TS として読み、選局し直しは殺して起動し直す。
package cmdbackend

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/devices"
)

const (
	// DefaultStopGrace は SIGTERM を送ってから SIGKILL するまで (6)。
	DefaultStopGrace = 5 * time.Second
	// DefaultFirstTSTimeout は最初の TS を待つ時間 (8.4)。
	DefaultFirstTSTimeout = 30 * time.Second

	devicePrefix = "cmd:"
)

// Config は Backend の設定。
type Config struct {
	// Defs は機材の定義 (組み込み + devices.json)。command のある機種だけ使う。
	Defs *devices.Defs
	// FakeTune は環境変数 FAKE_TUNE。書いてあれば、device の無いチューナーを全部 fake 機種として扱う。
	FakeTune string
	Log      backend.Logf

	StopGrace      time.Duration
	FirstTSTimeout time.Duration
}

// Backend は cmd バックエンド。
type Backend struct {
	cfg  Config
	defs *devices.Defs
}

var _ backend.Backend = (*Backend)(nil)

// New は Backend を作る。FakeTune があれば fake 機種を表に足す。
func New(cfg Config) *Backend {
	if cfg.Log == nil {
		cfg.Log = func(string, ...any) {}
	}
	if cfg.StopGrace <= 0 {
		cfg.StopGrace = DefaultStopGrace
	}
	if cfg.FirstTSTimeout <= 0 {
		cfg.FirstTSTimeout = DefaultFirstTSTimeout
	}
	defs := devices.NewDefs()
	if cfg.Defs != nil {
		for _, d := range cfg.Defs.All() {
			defs.Put(d)
		}
	}
	if cfg.FakeTune != "" {
		defs.Put(devices.Fake(cfg.FakeTune))
	}
	return &Backend{cfg: cfg, defs: defs}
}

// Detect は何も見つけない。cmd の機材は tuners.json に device を手で書く (6)。
func (b *Backend) Detect(*devices.Defs) ([]backend.TunerSpec, error) { return nil, nil }

// Open は device (`cmd:<model>:<デバイス>`) からチューナーを作る。
// 子プロセスは選局のときに起動するので、ここでは何も起動しない。
func (b *Backend) Open(spec backend.TunerSpec) (backend.Tuner, error) {
	def, device, err := b.resolve(spec)
	if err != nil {
		return nil, err
	}
	if err := checkDeviceHost(def, device); err != nil {
		return nil, backend.BadRequest("%s: %s", spec.Name, err.Error())
	}
	return newTuner(b, spec, def, device), nil
}

// Handles は device が cmd バックエンドのものか (`PUT /denpa/tuners` の検査にも使う)。
func (b *Backend) Handles(spec backend.TunerSpec) bool {
	if spec.Device == "" {
		return b.cfg.FakeTune != ""
	}
	return strings.HasPrefix(spec.Device, devicePrefix)
}

// Validate は `PUT /denpa/tuners` で受ける cmd: の device の検査。
// `<model>` は機材の定義にある command 付きの機種に限る (10.4)。
func (b *Backend) Validate(device string) error {
	def, dev, err := b.resolve(backend.TunerSpec{Device: device})
	if err != nil {
		return errors.New("cmd: の機種が機材の定義にありません (command のある機種だけ使えます)")
	}
	return checkDeviceHost(def, dev)
}

// hostRe はホスト名 (RFC 1123) の形。IPv4 もこの形に収まる。
var hostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

// validHost はホスト名か IPv4 か。長さは 253 まで。
func validHost(s string) bool { return len(s) > 0 && len(s) <= 253 && hostRe.MatchString(s) }

// checkDeviceHost は、command の引数の一部に {device} が入っている機種で、
// device の末尾がホスト名か IPv4 かを確かめる。{device} が URL の途中に入るので、
// `/`・`#`・`?`・`@`・`:`・空白が入ると、接続先のホスト・ポート・パスを変えられてしまう。
// {device} だけの要素は別の引数として渡るので、検証しない。
func checkDeviceHost(def *devices.Def, device string) error {
	for _, a := range def.Command {
		if a != "{device}" && strings.Contains(a, "{device}") {
			if !validHost(device) {
				return fmt.Errorf("device の末尾はホスト名か IPv4 だけにします (%s)", device)
			}
			return nil
		}
	}
	return nil
}

// resolve は device 文字列から機種とデバイス部分を決める。
// model にもデバイスにも `:` が入りうる (`nasne.local:8301`) ので、
// 機種は「`<model>:` で始まる中でいちばん長いもの」を取る。
func (b *Backend) resolve(spec backend.TunerSpec) (*devices.Def, string, error) {
	if spec.Device == "" {
		if b.cfg.FakeTune == "" {
			return nil, "", backend.BadRequest("%s: device がありません", spec.Name)
		}
		def, _ := b.defs.Get(devices.ModelFake)
		return def, "", nil
	}
	rest, ok := strings.CutPrefix(spec.Device, devicePrefix)
	if !ok {
		return nil, "", backend.BadRequest("%s: device は cmd: で始まる形にします (%s)", spec.Name, spec.Device)
	}
	var best *devices.Def
	all := b.defs.All()
	for i := range all {
		d := &all[i]
		if !d.IsCmd() || !strings.HasPrefix(rest, d.Model+":") {
			continue
		}
		if best == nil || len(d.Model) > len(best.Model) {
			best = d
		}
	}
	if best == nil {
		return nil, "", backend.BadRequest("%s: 機材の定義に command のある機種がありません (%s)", spec.Name, spec.Device)
	}
	return best, rest[len(best.Model)+1:], nil
}
