// Package devices は機材の定義 (devices.json) を持つ。仕様書 3 章と 6.1・6.4。
//
// 組み込みの表のうち px4_drv の機種 (chardev・host) はそれぞれのバックエンドを
// 作るときに足す。ここにあるのは cmd バックエンドの機種 (fake) と、
// 定義の読み方・重ね方・書き損じの扱い。
package devices

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

// ChannelFormat はチャンネル名の直し方 (6.2)。
const (
	FormatRecpt1  = "recpt1"
	FormatRecisdb = "recisdb"
	FormatPx4rec  = "px4rec"
	FormatDenpa   = "denpa"
)

// ModelFake は FAKE_TUNE のための組み込みの機種名。
const ModelFake = "fake"

var allTypes = []string{"GR", "BS", "CS", "CATV", "SKY"}

// Receiver は筐体の中の受信機 1 つ。
type Receiver struct {
	Index   int      `json:"index"`
	Types   []string `json:"types"`
	Systems []string `json:"systems,omitempty"`
}

// Def は機材の定義の 1 項目。
type Def struct {
	Model     string     `json:"model"`
	USB       []string   `json:"usb,omitempty"`
	Linux     *Linux     `json:"linux,omitempty"`
	Host      *Host      `json:"host,omitempty"`
	Receivers []Receiver `json:"receivers,omitempty"`
	// LNB は給電できる電圧。nil は「書いていない」、空は「給電できない」。
	LNB          []int `json:"lnb,omitempty"`
	CardReader   bool  `json:"cardReader,omitempty"`
	PairBySerial bool  `json:"pairBySerial,omitempty"`
	// Denpa は denpa 付属のエージェントが自分で開く機種。自動検出の既定の対象から外す (DETECT_INCLUDE で対象にできる)。
	// 組み込みの機種は、USB の ID で印を付ける。devices.json では、書いたとおりになる (書かなければ偽)
	Denpa bool `json:"denpa,omitempty"`

	// cmd バックエンド (6.1)
	Command []string `json:"command,omitempty"`
	// URL は curl を使わずに、エージェントが HTTP の GET で TS を直接読む機種 (6.5)。command とは同時に書けない。
	// {device}・{channel}・{sidQuery} などは command と同じに展開する。503 は使用中 (409)、404 は同期しなかった。
	URL           string              `json:"url,omitempty"`
	ChannelFormat string              `json:"channelFormat,omitempty"`
	LnbArg        map[string][]string `json:"lnbArg,omitempty"`
	Env           map[string]string   `json:"env,omitempty"`

	// 1 サービスしか出ない機材と機材の中で復号する機材 (6.4)
	SingleService     bool     `json:"singleService,omitempty"`
	DecryptedByDevice bool     `json:"decryptedByDevice,omitempty"`
	ServiceArg        []string `json:"serviceArg,omitempty"`
	TsidArg           []string `json:"tsidArg,omitempty"`
	BusyExitCode      *int     `json:"busyExitCode,omitempty"`

	Notes string `json:"notes,omitempty"`
}

// Linux は chardev の項目。
type Linux struct {
	Devname string `json:"devname"`
}

// Host は host の項目。
type Host struct {
	DeviceGUID string `json:"deviceGuid"`
	// InterfaceGUID は DriverHost_PX4.ini の `DeviceInterfaceGUID`。WinUSB が入っている機材を数える鍵 (Windows の自動検出)。
	// 書かなければ、USB の ID (`usb`) だけで数える
	InterfaceGUID string   `json:"interfaceGuid,omitempty"`
	ReceiverGUIDs []string `json:"receiverGuids,omitempty"`
}

// IsCmd は cmd バックエンドの機種か (command を起動するものと、url を HTTP で読むもの)。
func (d *Def) IsCmd() bool { return len(d.Command) > 0 || d.URL != "" }

// IsHTTP は HTTP で直接読む機種か。
func (d *Def) IsHTTP() bool { return d.URL != "" }

// Receivable はその機材が受けられる種別 (cmd は受信機 1 つ)。
func (d *Def) Receivable() []string {
	var out []string
	for _, r := range d.Receivers {
		out = append(out, r.Types...)
	}
	return out
}

// CanReceive は種別を受けられるか。
func (d *Def) CanReceive(typ string) bool {
	for _, t := range d.Receivable() {
		if t == typ {
			return true
		}
	}
	return false
}

// SupportsLnb は電圧 (11・15) を出せるか。lnb の記述が無ければ制限しない。
func (d *Def) SupportsLnb(voltage int) bool {
	if d.LNB == nil || voltage == 0 {
		return true
	}
	for _, v := range d.LNB {
		if v == voltage {
			return true
		}
	}
	return false
}

// Validate は書き損じを調べる。cmd の機種は 6.1 の決まりも見る。
func (d *Def) Validate() error {
	if strings.TrimSpace(d.Model) == "" {
		return errors.New("model がありません")
	}
	for _, r := range d.Receivers {
		for _, t := range r.Types {
			if !validType(t) {
				return fmt.Errorf("receivers の types に読めない種別があります (%s)", t)
			}
		}
	}
	if !d.IsCmd() {
		return nil
	}
	if d.IsHTTP() {
		if len(d.Command) > 0 {
			return errors.New("command と url は同時に書けません")
		}
		if !strings.HasPrefix(d.URL, "http://") || len(d.URL) <= len("http://") {
			return fmt.Errorf("url は http:// で始めます (%s)", d.URL)
		}
		if len(d.ServiceArg) > 0 || len(d.TsidArg) > 0 || d.BusyExitCode != nil {
			return errors.New("url のときは serviceArg・tsidArg・busyExitCode は使いません (局と使用中の判定は URL と HTTP のステータスで決まります)")
		}
	} else if strings.TrimSpace(d.Command[0]) == "" {
		return errors.New("command の先頭 (実行ファイル) が空です")
	}
	switch d.ChannelFormat {
	case FormatRecpt1, FormatRecisdb, FormatPx4rec, FormatDenpa:
	case "":
		return errors.New("channelFormat がありません (recpt1・recisdb・px4rec・denpa のどれか)")
	default:
		return fmt.Errorf("channelFormat を読めません (%s)", d.ChannelFormat)
	}
	if len(d.Receivers) != 1 {
		return errors.New("command のある機種は receivers を 1 つだけ書きます")
	}
	if len(d.Receivers[0].Types) == 0 {
		return errors.New("receivers の types がありません")
	}
	if d.ChannelFormat == FormatPx4rec {
		// px4rec の `T 27` は 2 つの引数になるので、{channel} は 1 要素まるごとに書く
		for _, a := range d.Command {
			if a != "{channel}" && strings.Contains(a, "{channel}") {
				return errors.New("px4rec のときは {channel} だけの要素にします (引数が 2 つ以上になるため)")
			}
		}
	}
	return nil
}

func validType(t string) bool {
	for _, v := range allTypes {
		if v == t {
			return true
		}
	}
	return false
}

// fakeCommand は FAKE_TUNE をコマンドにする。適合テストは `bun <スクリプト>` のように
// 引数つきのコマンドラインを渡すので、空白で分ける。ただし、その値そのものが
// 存在するファイルなら (空白を含むパス)、分けずに 1 つの実行ファイルとして扱う。
func fakeCommand(s string) []string {
	if _, err := os.Stat(s); err == nil {
		return []string{s}
	}
	if f := strings.Fields(s); len(f) > 0 {
		return f
	}
	return []string{s}
}

// Fake は FAKE_TUNE の機種。`<FAKE_TUNE> <種別> <チャンネル名>` を起動する (6.3)。
func Fake(path string) Def {
	return Def{
		Model:         ModelFake,
		Command:       append(fakeCommand(path), "{type}", "{channel}"),
		ChannelFormat: FormatDenpa,
		Receivers:     []Receiver{{Index: 0, Types: append([]string(nil), allTypes...)}},
	}
}

// Defs は機材の定義の表。定義の順を保つ。
type Defs struct {
	list []Def

	mu           sync.Mutex // 以下は、検出 (別の goroutine) と --devices から触る
	include      map[string]bool
	includeDenpa bool
	skipped      map[string]map[string]struct{}
}

// NewDefs は空の表を作る。
func NewDefs() *Defs { return &Defs{} }

// Put は model をキーに、あれば上書き、無ければ末尾に足す。上書きしたら true。
func (s *Defs) Put(d Def) (overridden bool) {
	for i := range s.list {
		if s.list[i].Model == d.Model {
			s.list[i] = d
			return true
		}
	}
	s.list = append(s.list, d)
	return false
}

// Get は model で引く。
func (s *Defs) Get(model string) (*Def, bool) {
	for i := range s.list {
		if s.list[i].Model == model {
			return &s.list[i], true
		}
	}
	return nil, false
}

// All は定義の順に返す。
func (s *Defs) All() []Def { return append([]Def(nil), s.list...) }

// LoadFile は devices.json の項目を読む。ファイルが無ければ何も返さない。
// 読めない・形式が違うファイルは記録に出して無いものとし、1 項目の書き損じはその項目だけ飛ばす (12.2)。
func LoadFile(path string, logf func(format string, args ...any)) []Def {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logf("devices.json を読めません (%s): %v", path, err)
		}
		return nil
	}
	var file struct {
		Devices []json.RawMessage `json:"devices"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		logf("devices.json の形式が違います (%s): %v", path, err)
		return nil
	}
	var out []Def
	for i, raw := range file.Devices {
		var d Def
		if err := json.Unmarshal(raw, &d); err != nil {
			logf("devices.json の %d 番目を読めません。この項目は飛ばします: %v", i+1, err)
			continue
		}
		if err := d.Validate(); err != nil {
			name := d.Model
			if name == "" {
				name = fmt.Sprintf("%d 番目", i+1)
			}
			logf("devices.json の %s: %v。この項目は飛ばします", name, err)
			continue
		}
		out = append(out, d)
	}
	return out
}

// Overlay は items を表に重ね、足した機種と上書きした機種を記録に出す。
func (s *Defs) Overlay(items []Def, logf func(format string, args ...any)) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	for _, d := range items {
		if s.Put(d) {
			logf("devices.json で機種を上書きしました: %s", d.Model)
		} else {
			logf("devices.json で機種を足しました: %s", d.Model)
		}
	}
}

// Resolved は tuners.json の device 文字列から引いた機種 (`--devices` の表示用)。
type Resolved struct {
	// Models は機種名。1 つに決まれば 1 つ (Exact)、決まらなければ候補 (px4: は devname を複数の機種が共有する)。
	// 引けなければ空
	Models []string
	Exact  bool
	// Receiver は受信機の番号。px4host: だけ device に入っている。無ければ -1
	Receiver int
	// Note は引けなかった・特別な扱いの理由 (空なら無し)
	Note string
}

// ModelOf は device 文字列から機種を引く。実際に開くときの解釈 (host.Open・cmdbackend の resolve) と同じ規則。
//   - `px4host:<GUID>:<受信機の番号>:<台目>` は、host.deviceGuid が同じ機種
//   - `cmd:<model>:…` は、`<model>:` で始まる cmd の機種のうちいちばん長いもの
//   - `px4:/dev/<devname><N>` は、linux.devname が同じ機種 (複数あれば、候補を全部返す。USB の ID で決めるのは検出のとき)
//   - 空の device は、FAKE_TUNE のときの偽の機種 (fake)
//
// 機材は開かず、ファイルも見ない。
func (s *Defs) ModelOf(device string) Resolved {
	r := Resolved{Receiver: -1}
	switch {
	case device == "":
		r.Models, r.Exact, r.Note = []string{ModelFake}, true, "device が空 (FAKE_TUNE のときだけ使える)"
	case strings.HasPrefix(device, "px4host:"):
		parts := strings.Split(strings.TrimPrefix(device, "px4host:"), ":")
		guid := strings.ToLower(strings.Trim(parts[0], "{}"))
		for _, d := range s.list {
			if d.Host != nil && strings.ToLower(strings.Trim(d.Host.DeviceGUID, "{}")) == guid {
				r.Models, r.Exact = []string{d.Model}, true
				break
			}
		}
		if len(parts) >= 2 {
			if n, err := strconv.Atoi(parts[1]); err == nil && n >= 0 {
				r.Receiver = n
			}
		}
		if !r.Exact {
			r.Note = "この GUID の機種が機材の定義にありません"
		}
	case strings.HasPrefix(device, "cmd:"):
		rest := strings.TrimPrefix(device, "cmd:")
		var best *Def
		for i := range s.list {
			d := &s.list[i]
			if d.IsCmd() && strings.HasPrefix(rest, d.Model+":") && (best == nil || len(d.Model) > len(best.Model)) {
				best = d
			}
		}
		if best != nil {
			r.Models, r.Exact = []string{best.Model}, true
		} else {
			r.Note = "この機種が cmd の定義にありません"
		}
	case strings.HasPrefix(device, "px4:"):
		base := device[strings.LastIndexAny(device, `/\`)+1:]
		name := strings.TrimRight(base, "0123456789")
		for _, d := range s.list {
			if d.Linux != nil && d.Linux.Devname != "" && d.Linux.Devname == name {
				r.Models = append(r.Models, d.Model)
			}
		}
		r.Exact = len(r.Models) == 1
		if len(r.Models) == 0 {
			r.Note = "この devname の機種が機材の定義にありません"
		}
	default:
		r.Note = "px4:・px4host:・cmd: で始まらない形 (このエージェントは開かない)"
	}
	return r
}
