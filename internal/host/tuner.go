package host

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/channel"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/devices"
)

// DevicePrefix は tuners.json の device の接頭辞。
const DevicePrefix = "px4host:"

const tuneTimeoutMs = 5000

// Tuner は DriverHost_PX4 の受信機 1 つ。制御のパイプを 1 本持ち、最初の選局で開いて手放すまで開いたままにする。
type Tuner struct {
	spec    backend.TunerSpec
	tr      Transport
	def     *devices.Def
	guid    [16]byte
	index   int
	systems uint32
	log     backend.Logf

	mu        sync.Mutex
	ctrl      *rpc
	feed      *dataFeed
	reader    *feedReader
	dataID    uint32
	capturing bool
	lnb       int
	sat       bool
	hasCh     bool
}

var _ backend.Tuner = (*Tuner)(nil)
var _ backend.Receivabler = (*Tuner)(nil)

// systemsOf は種別から方式のビット和 (receivers[].systems があればそれを使う)。
func systemsOf(r devices.Receiver) uint32 {
	var s uint32
	if len(r.Systems) > 0 {
		for _, n := range r.Systems {
			switch n {
			case "ISDB-T":
				s |= SystemT
			case "ISDB-S":
				s |= SystemS
			case "ISDB-C":
				s |= SystemC
			case "J.83B":
				s |= SystemJ83
			}
		}
		return s
	}
	for _, t := range r.Types {
		switch t {
		case "GR":
			s |= SystemT
		case "BS", "CS":
			s |= SystemS
		case "CATV":
			s |= SystemC
		case "SKY":
			s |= SystemJ83
		}
	}
	return s
}

// Open は device (`px4host:<GUID>:<受信機の index>:<台目>`) からチューナーを作る。繋ぐのは選局のとき。
func Open(defs *devices.Defs, tr Transport, log backend.Logf, spec backend.TunerSpec) (backend.Tuner, error) {
	rest, ok := strings.CutPrefix(spec.Device, DevicePrefix)
	if !ok {
		return nil, fmt.Errorf("device は %s で始めます", DevicePrefix)
	}
	parts := strings.Split(rest, ":")
	if len(parts) < 2 {
		return nil, fmt.Errorf("device は %s<GUID>:<受信機の番号>:<台目> にします", DevicePrefix)
	}
	guid, err := GUID(parts[0])
	if err != nil {
		return nil, err
	}
	index, err := strconv.Atoi(parts[1])
	if err != nil || index < 0 {
		return nil, fmt.Errorf("受信機の番号を読めません (%s)", parts[1])
	}
	var def *devices.Def
	all := defs.All()
	for i := range all {
		if all[i].Host != nil && strings.EqualFold(strings.Trim(all[i].Host.DeviceGUID, "{}"), strings.Trim(parts[0], "{}")) {
			def = &all[i]
			break
		}
	}
	if def == nil {
		return nil, fmt.Errorf("GUID %s の機種が機材の定義にありません。DriverHost_PX4.ini と devices.json の両方に書きます", parts[0])
	}
	var rcv *devices.Receiver
	for i := range def.Receivers {
		if def.Receivers[i].Index == index {
			rcv = &def.Receivers[i]
		}
	}
	if rcv == nil {
		return nil, fmt.Errorf("%s に受信機 %d がありません", def.Model, index)
	}
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Tuner{spec: spec, tr: tr, def: def, guid: guid, index: index, systems: systemsOf(*rcv), log: log}, nil
}

func (t *Tuner) rcvTypes() []string {
	for _, r := range t.def.Receivers {
		if r.Index == t.index {
			return r.Types
		}
	}
	return nil
}

// Receivable は受信機の types。
func (t *Tuner) Receivable() []string { return append([]string{}, t.rcvTypes()...) }

func (t *Tuner) name() string { return t.spec.Name }

// resetLocked は開いていない状態に戻す (パイプが切れたとき)。
func (t *Tuner) resetLocked() {
	if t.ctrl != nil {
		t.ctrl.close()
		t.ctrl = nil
	}
	if t.feed != nil {
		t.feed.close()
		t.feed = nil
	}
	if t.reader != nil {
		t.reader.halt()
		t.reader = nil
	}
	t.capturing, t.hasCh = false, false
}

func (t *Tuner) call(req []byte) ([]byte, error) {
	resp, err := t.ctrl.call(req)
	if err != nil {
		if isBroken(err) {
			t.resetLocked()
			return nil, fmt.Errorf("%s: %w", t.name(), backend.ErrUnplugged)
		}
		return nil, err
	}
	return resp, nil
}

func (t *Tuner) ensureOpen() error {
	if t.ctrl != nil {
		return nil
	}
	l := t.tr.Layout()
	c, err := t.tr.Dial(CtrlPipe)
	if err != nil {
		return err
	}
	t.ctrl = &rpc{conn: c}
	resp, err := t.call(versionReq())
	if err != nil {
		t.resetLocked()
		return err
	}
	if v := binary.LittleEndian.Uint32(resp[12:]); respStatus(resp) != statusOK || v != cmdVersionValue {
		t.resetLocked()
		return fmt.Errorf("DriverHost_PX4 のコマンドのバージョンが違います (0x%08X。0x%08X が必要です)", v, cmdVersionValue)
	}
	resp, err = t.call(openReq(l, t.guid, int32(t.index), t.systems))
	if err != nil {
		t.resetLocked()
		return err
	}
	if respStatus(resp) != statusOK {
		t.resetLocked()
		return fmt.Errorf("%s: 受信機を開けません (DriverHost_PX4 に空きがありません)", t.name())
	}
	t.dataID = openDataID(l, resp)
	return t.ensureData()
}

// ensureData は TS のパイプを繋いで SET_DATA_ID を書く。SET_CAPTURE の前に繋ぐ。
// 繋ぎ直すと DriverHost_PX4 に切られるので、受信機を手放すまで開いたままにする (feed.go)。
func (t *Tuner) ensureData() error {
	if t.feed != nil {
		return nil
	}
	d, err := t.tr.Dial(DataPipe)
	if err != nil {
		t.resetLocked()
		return err
	}
	if _, err := d.Write(dataCmd(dataSetDataID, t.dataID)); err != nil {
		d.Close()
		t.resetLocked()
		return fmt.Errorf("%s: TS のパイプに書けません (%v)", t.name(), err)
	}
	t.feed = newDataFeed(d)
	return nil
}

// Tune は 5.4 の手順。
func (t *Tuner) Tune(tn backend.Tuning) error {
	ch, err := channel.Parse(tn.Channel)
	if err != nil {
		return backend.BadRequest("%s", err.Error())
	}
	if !contains(t.rcvTypes(), string(ch.Type)) {
		return backend.BadRequest("%s: %s は受けられません", t.name(), ch.Type)
	}
	sat := ch.Type == channel.BS || ch.Type == channel.CS
	t.mu.Lock()
	defer t.mu.Unlock()
	if sat && t.lnb != 0 && !t.def.SupportsLnb(t.lnb) {
		return &backend.StatusError{Code: 400, Msg: fmt.Sprintf("%s: この機材は %dv を出せません", t.name(), t.lnb), Err: backend.ErrUnsupported}
	}
	if t.feed != nil && t.feed.isDead() { // DriverHost_PX4 が閉じた。受信機から開き直す
		t.resetLocked()
	}
	if err := t.ensureOpen(); err != nil {
		return t.wrapOpen(err)
	}
	// BonDriver は受信を最初に始めてから選局する。止めて始め直す手順では、再開した DT295 がロックしなかった (実機)
	if !t.capturing {
		resp, err := t.call(captureReq(true))
		if err != nil {
			return err
		}
		if respStatus(resp) != statusOK {
			return fmt.Errorf("%s: 受信を始められません", t.name())
		}
		t.capturing = true
	}
	if sat && len(t.def.LNB) > 0 { // lnb の無い機種では送らない
		resp, err := t.call(lnbReq(int32(t.lnb)))
		if err != nil {
			return err
		}
		if respStatus(resp) != statusOK {
			return fmt.Errorf("%s: 給電を切り替えられません", t.name())
		}
	}

	v := ch.Values()
	var params []Param
	switch v.System {
	case uint32(SystemS):
		params = []Param{{paramStreamID, uint32(v.StreamID)}}
	case uint32(SystemC):
		params = []Param{{paramBandwidth, 6}, {paramTSMFStream, uint32(v.Stream)}}
	}
	resp, err := t.call(paramsReq(v.System, v.FreqKHz, params))
	if err != nil {
		return err
	}
	if respStatus(resp) != statusOK {
		return fmt.Errorf("%s: 方式と周波数を指定できません (%s)", t.name(), ch)
	}
	resp, err = t.call(tuneReq(tuneTimeoutMs))
	if err != nil {
		return err
	}
	if respStatus(resp) != statusOK { // TSMF の TS が無いときも失敗するだけで区別できない
		return fmt.Errorf("%s: %w", t.name(), backend.ErrNoLock)
	}
	t.sat, t.hasCh = sat, true
	// 前のチャンネルの残りを捨てる。PURGE のあとの 1 塊も捨てる
	t.feed.drain()
	t.feed.skip.Store(1)
	if _, err := t.feed.conn.Write(dataCmd(dataPurge, 0)); err != nil {
		t.resetLocked()
		return fmt.Errorf("%s: %w", t.name(), backend.ErrUnplugged)
	}
	return nil
}

func (t *Tuner) wrapOpen(err error) error {
	var nc *ErrNotConnected
	if errors.As(err, &nc) {
		return err
	}
	return err
}

// Start は TS のパイプの読み出し。選局ごとの読み手を返す。
func (t *Tuner) Start() (io.ReadCloser, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.capturing || t.feed == nil {
		return nil, fmt.Errorf("%s: 選局していません", t.name())
	}
	if t.reader != nil {
		t.reader.halt()
	}
	t.reader = newFeedReader(t.feed, t.name())
	return t.reader, nil
}

// Stop は読み手を外す。TS のパイプは閉じず、受信も止めない。
func (t *Tuner) Stop() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	// SET_CAPTURE(false) は送らない。BonDriver も選局の間は止めず、止めて再開した DT295 は
	// ロックしなかった (実機)。受信を止めるのは Close のときだけ
	if t.reader != nil {
		t.reader.halt()
		t.reader = nil
	}
	if t.feed != nil {
		t.feed.drain()
	}
	t.hasCh = false
	return nil
}

// Signal は C/N (READ_STATS の CNR。mdB) と、ロック (CHECK_LOCK)。
func (t *Tuner) Signal() (*backend.TunerSignal, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ctrl == nil || !t.capturing || !t.hasCh {
		return nil, nil
	}
	sig := &backend.TunerSignal{Unit: "dB"}
	if resp, err := t.call(checkLockReq()); err == nil && respStatus(resp) == statusOK {
		sig.Locked = resp[8] != 0
	}
	if resp, err := t.call(statsReq(statCNR)); err == nil && respStatus(resp) == statusOK {
		if binary.LittleEndian.Uint32(resp[8:]) >= 1 && binary.LittleEndian.Uint32(resp[12:]) == statCNR {
			db := float64(int32(binary.LittleEndian.Uint32(resp[16:]))) / 1000
			sig.CN = &db
		}
	}
	return sig, nil
}

// SetLnb は次の選局の給電。
func (t *Tuner) SetLnb(voltage int) error {
	switch voltage {
	case 0, 11, 15:
	default:
		return backend.BadRequest("%s: 電圧は 0・11・15 のどれかにします (%d)", t.name(), voltage)
	}
	if !t.def.SupportsLnb(voltage) {
		return &backend.StatusError{Code: 400, Msg: fmt.Sprintf("%s: この機材は %dv を出せません", t.name(), voltage), Err: backend.ErrUnsupported}
	}
	t.mu.Lock()
	t.lnb = voltage
	t.mu.Unlock()
	return nil
}

// Close は SET_CAPTURE(false) → SET_LNB_VOLTAGE(0) → CLOSE → パイプを閉じる (13)。
func (t *Tuner) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ctrl != nil {
		if t.capturing {
			t.call(captureReq(false))
		}
		if t.ctrl != nil && len(t.def.LNB) > 0 && t.lnb != 0 {
			t.call(lnbReq(0))
		}
		if t.ctrl != nil {
			t.call(closeReq())
		}
	}
	t.resetLocked()
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
