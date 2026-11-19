// Package backend は機材に触る層のインターフェース (仕様書 2 章) と、選局の失敗の型を持つ。
package backend

import (
	"errors"
	"fmt"
	"io"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/devices"
)

// Logf は記録に 1 行出す。`[agent] ` は出す側が付ける。
type Logf func(format string, args ...any)

// TunerSpec はチューナー 1 つの定義 (tuners.json の 1 台)。
type TunerSpec struct {
	Name     string   `json:"name"`
	Types    []string `json:"types"`
	Device   string   `json:"device"`
	Lnb      string   `json:"lnb,omitempty"`
	Disabled bool     `json:"disabled"`
}

// Tuning は 1 回の選局の要求。
type Tuning struct {
	Type    string // GR / BS / CS / CATV / SKY。空ならチャンネル名から決める
	Channel string // T27・BS15:16625 など
	// Service は観る局の service_id。0 は指定なし。
	Service int
	// NeedsKey は鍵の要る利用者 (rec・live) の選局か。singleService の機材で局を必須にする。
	NeedsKey bool
}

// TunerSignal は受信の状態 (7.4)。
type TunerSignal struct {
	Locked      bool     `json:"locked"`
	CN          *float64 `json:"cn"`
	Unit        string   `json:"unit"`
	AtTune      bool     `json:"atTune"`
	StrengthDbm *float64 `json:"strengthDbm"`
}

// Backend は機材に触る層。
type Backend interface {
	// Detect は挿さっている機材を機材の定義と突き合わせて並べる (自動検出)。
	Detect(defs *devices.Defs) ([]TunerSpec, error)
	// Open は device 文字列からチューナーを開く。開いたままにする。
	Open(spec TunerSpec) (Tuner, error)
}

// Tuner は受信機 1 つ。
type Tuner interface {
	// Tune は選局する。開いたまま何度でも呼べる。同期しなければ ErrNoLock。
	Tune(t Tuning) error
	// Start は TS (か TLV) の読み出しを返す。Tune のあとで取る。
	Start() (io.ReadCloser, error)
	Stop() error
	// Signal は受信の状態と C/N。受けていなければ nil。
	Signal() (*TunerSignal, error)
	// SetLnb は 0 / 11 / 15。対応しない機材は ErrUnsupported。
	SetLnb(voltage int) error
	Close() error
}

// Receivabler は受けられる種別を返せるバックエンド。デバイスが見つかっていなければ nil。
type Receivabler interface{ Receivable() []string }

// DecryptedByDevicer は機材の中で復号する機材 (6.4)。
type DecryptedByDevicer interface{ DecryptedByDevice() bool }

// SingleServicer は 1 サービスしか出ない機材 (6.4)。
type SingleServicer interface{ SingleService() bool }

// Passthroughers は復号の経路を通さずに流す機材 (FAKE_TUNE)。
type Passthroughers interface{ Passthrough() bool }

// ErrUnplugged は USB が抜かれた・パイプが切れたとき。
var ErrUnplugged = errors.New("機材が抜かれました")

// 選局の失敗。文言は仕様書 7.7 のとおり。
var (
	// ErrNoLock の文言は denpa が放送休止の判定に読むので変えない。
	ErrNoLock      = errors.New("同期しませんでした (電波が来ていないか、その周波数に放送がありません)")
	ErrNoTS        = errors.New("30 秒待っても TS が届きませんでした")
	ErrBusy        = errors.New("チューナーに空きがありません")
	ErrUnsupported = errors.New("この機材では対応していません")
	ErrAborted     = errors.New("選局を中断しました")
)

// StatusError は HTTP の状態つきの失敗。
type StatusError struct {
	Code int
	Msg  string
	Err  error
}

func (e *StatusError) Error() string { return e.Msg }
func (e *StatusError) Unwrap() error { return e.Err }

// BadRequest は 400。
func BadRequest(format string, args ...any) *StatusError {
	return &StatusError{Code: 400, Msg: fmt.Sprintf(format, args...)}
}

// StatusOf は err を返すときの HTTP の状態。StatusError でなければ 500。
func StatusOf(err error) int {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Code
	}
	return 500
}
