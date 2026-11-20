//go:build linux

package chardev

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/channel"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/devices"
)

const readSize = 188 * 816 // IT930x の転送 1 回ぶん (4.3)

// Tuner は px4_drv のデバイスファイル 1 つ。最初の選局で開き、手放すまで開いたままにする。
type Tuner struct {
	spec  backend.TunerSpec
	path  string
	def   *devices.Def
	index int

	mu        sync.Mutex
	f         *os.File
	streaming bool
	lnb       int
	sat       bool
	ch        channel.Channel
	hasCh     bool
	rd        *devReader
	lastRead  atomic.Int64
}

var _ backend.Tuner = (*Tuner)(nil)
var _ backend.Receivabler = (*Tuner)(nil)

// Open は tuners.json の device (`px4:/dev/px4video0`) からチューナーを作る。デバイスは選局のときに開く。
func Open(defs *devices.Defs, sysDir string, spec backend.TunerSpec) (backend.Tuner, error) {
	path := strings.TrimPrefix(spec.Device, DevicePrefix)
	if path == spec.Device || path == "" {
		return nil, fmt.Errorf("device は %s で始めます", DevicePrefix)
	}
	name := path[strings.LastIndex(path, "/")+1:]
	f := &devfile{name: name}
	f.num, _ = strconv.Atoi(name[len(devname(name)):])
	f.usb = readUSB(sysDir, name)
	all := defs.All()
	f.def = pickDef(all, f)
	if f.def == nil {
		return nil, fmt.Errorf("%s に当たる機種が機材の定義にありません", name)
	}
	t := &Tuner{spec: spec, path: path, def: f.def}
	// 筐体の中の番号は名前の `#<受信機>` から。無ければデバイスファイルの番号
	t.index = f.num
	if i := strings.LastIndex(spec.Name, "#"); i >= 0 {
		if n, err := strconv.Atoi(spec.Name[i+1:]); err == nil {
			t.index = n
		}
	}
	return t, nil
}

// Receivable は受信機の types。デバイスファイルが無ければ nil (抜いてある)。
func (t *Tuner) Receivable() []string {
	if _, err := os.Stat(t.path); err != nil {
		return nil
	}
	if len(t.def.Receivers) == 0 {
		return []string{}
	}
	return append([]string{}, t.def.Receivers[t.index%len(t.def.Receivers)].Types...)
}

func ioctl(f *os.File, req uintptr, arg uintptr) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, arg)
	if e != 0 {
		return e
	}
	return nil
}

func (t *Tuner) wrap(err error) error {
	if errors.Is(err, syscall.EIO) {
		return backend.ErrUnplugged
	}
	return err
}

func (t *Tuner) ensureOpen() error {
	if t.f != nil {
		return nil
	}
	f, err := os.OpenFile(t.path, os.O_RDWR, 0)
	if err != nil {
		if errors.Is(err, syscall.EALREADY) || errors.Is(err, syscall.EBUSY) {
			return fmt.Errorf("%s: %s を開けません (%v)。ほかのソフトが使っています", t.spec.Name, t.path, err)
		}
		return fmt.Errorf("%s: %s を開けません (%v)", t.spec.Name, t.path, err)
	}
	t.f = f
	return nil
}

// Tune は 4.4 の手順。
func (t *Tuner) Tune(tn backend.Tuning) error {
	ch, err := channel.Parse(tn.Channel)
	if err != nil {
		return backend.BadRequest("%s", err.Error())
	}
	if !containsStr(t.Receivable(), string(ch.Type)) {
		return backend.BadRequest("%s: %s は受けられません", t.spec.Name, ch.Type)
	}
	sat := ch.Type == channel.BS || ch.Type == channel.CS

	t.mu.Lock()
	defer t.mu.Unlock()
	if sat && t.lnb != 0 && !t.def.SupportsLnb(t.lnb) {
		return &backend.StatusError{Code: 400, Msg: fmt.Sprintf("%s: この機材は %dv を出せません", t.spec.Name, t.lnb), Err: backend.ErrUnsupported}
	}
	if err := t.ensureOpen(); err != nil {
		return err
	}
	t.stopLocked()

	if sat { // 衛星でない選局では給電を触らない
		if err := t.setLnbLocked(t.lnb); err != nil {
			return err
		}
	}
	v := ch.Values()
	if err := t.tuneLocked(v, ch.ShiftedFreqKHz()); err != nil {
		return err
	}
	if err := ioctl(t.f, ptxStartStreaming, 0); err != nil && !errors.Is(err, syscall.EALREADY) {
		return fmt.Errorf("%s: 受信を始められません (%v)", t.spec.Name, t.wrap(err))
	}
	t.streaming, t.sat, t.ch, t.hasCh = true, sat, ch, true
	t.lastRead.Store(time.Now().UnixNano())
	return nil
}

// tuneLocked は方式と周波数を渡して、ロックを待つ (仕様書 4.4 の 3)。
//   - GR・BS・CS と ISDB-C の 0 本目: PTX_SET_SYSTEM_MODE → PTX_SET_CHANNEL
//   - ISDB-C の 1 本目以降と SKY (J.83B): PTXT_SET_PARAMS → PTXT_TUNE (PTX_SET_SYSTEM_MODE は J.83B を受けない)
//   - SKY は、元の周波数で同期しなければずらした周波数でもう一度 (仕様書 7.2)。shifted が 0 なら再試行しない。
func (t *Tuner) tuneLocked(v channel.Values, shifted uint32) error {
	if v.System == channel.SystemJ83 || (v.System == channel.SystemC && v.Stream > 0) {
		props := tsmfProps(v.System, v.Stream)
		tuneOnce := func(freq uint32) error {
			p := ptxtParams{System: v.System, Freq: freq, NumProp: uint32(len(props))}
			if len(props) > 0 {
				p.Prop = uint64(uintptr(unsafe.Pointer(&props[0])))
			}
			err := ioctl(t.f, ptxtSetParams, uintptr(unsafe.Pointer(&p)))
			if err == nil {
				err = ioctl(t.f, ptxtTune, 0)
			}
			runtime.KeepAlive(props)
			return err
		}
		err := tuneOnce(v.FreqKHz)
		if err != nil && v.System == channel.SystemJ83 && shifted != 0 && errors.Is(err, syscall.EAGAIN) {
			err = tuneOnce(shifted)
		}
		return t.tuneErr(err, v)
	}
	mode := uintptr(modeISDBT)
	switch v.System {
	case channel.SystemS:
		mode = modeISDBS
	case channel.SystemC:
		mode = modeISDBC
	}
	if err := ioctl(t.f, ptxSetSystemMode, mode); err != nil {
		return fmt.Errorf("%s: 方式を指定できません (%v)", t.spec.Name, t.wrap(err))
	}
	arg := [2]int32{int32(v.FreqNo), int32(v.Slot)}
	err := ioctl(t.f, ptxSetChannel, uintptr(unsafe.Pointer(&arg[0])))
	return t.tuneErr(err, v)
}

// tuneErr は選局の ioctl の失敗を、仕様書 7.7 の返し方に直す。
func (t *Tuner) tuneErr(err error, v channel.Values) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, syscall.EAGAIN):
		return fmt.Errorf("%s: %w", t.spec.Name, backend.ErrNoLock)
	case errors.Is(err, syscall.ENODATA): // TSMF に指定の TS が無い
		return fmt.Errorf("%s: TSMF に %d 本目の TS がありません", t.spec.Name, v.Stream)
	case errors.Is(err, syscall.EIO):
		return fmt.Errorf("%s: %w", t.spec.Name, backend.ErrUnplugged)
	}
	return fmt.Errorf("%s: 選局できません (%v)", t.spec.Name, err)
}

func (t *Tuner) setLnbLocked(v int) error {
	var err error
	switch v {
	case 0:
		err = ioctl(t.f, ptxDisableLnbPower, 0)
	case 11:
		err = ioctl(t.f, ptxEnableLnbPower, 1)
	case 15:
		err = ioctl(t.f, ptxEnableLnbPower, 2)
	}
	if errors.Is(err, syscall.ENOSYS) {
		if v == 0 {
			return nil
		}
		return &backend.StatusError{Code: 400, Msg: fmt.Sprintf("%s: この機材は給電できません", t.spec.Name), Err: backend.ErrUnsupported}
	}
	if err != nil {
		return fmt.Errorf("%s: 給電を切り替えられません (%v)", t.spec.Name, t.wrap(err))
	}
	return nil
}

// Start は read() の読み出し。読み出しは read() だけをする goroutine に任せる。
func (t *Tuner) Start() (io.ReadCloser, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.streaming || t.f == nil {
		return nil, fmt.Errorf("%s: 選局していません", t.spec.Name)
	}
	if t.rd != nil {
		return nil, fmt.Errorf("%s: 読み出しは選局ごとに 1 つです", t.spec.Name)
	}
	t.rd = newDevReader(t.f, &t.lastRead, t.wrap)
	return t.rd, nil
}

// Stop は PTX_STOP_STREAMING。
func (t *Tuner) Stop() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopLocked()
	return nil
}

func (t *Tuner) stopLocked() {
	if t.f == nil {
		return
	}
	if t.rd != nil {
		t.rd.halt()
	}
	if t.streaming {
		if err := ioctl(t.f, ptxStopStreaming, 0); err != nil && !errors.Is(err, syscall.EALREADY) && !errors.Is(err, syscall.EIO) {
			_ = err
		}
	}
	if t.rd != nil {
		// read() が戻らなければ、デバイスを閉じて次の選局で開き直す
		if !t.rd.wait(time.Second) {
			t.f.Close()
			t.f = nil
		}
		t.rd = nil
	}
	t.streaming, t.hasCh = false, false
}

// Signal は C/N。locked は「TS が 5 秒以内に届いている」で決める (ロックの ioctl が無い)。
func (t *Tuner) Signal() (*backend.TunerSignal, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.streaming || t.f == nil || !t.hasCh {
		return nil, nil
	}
	sig := &backend.TunerSignal{Unit: "dB"}
	sig.Locked = time.Since(time.Unix(0, t.lastRead.Load())) < 5*time.Second
	var raw uint32
	if err := ioctl(t.f, ptxGetCnr, uintptr(unsafe.Pointer(&raw))); err == nil {
		if db, ok := CNR(raw, t.sat); ok {
			sig.CN = &db
		}
	}
	return sig, nil
}

// SetLnb は次の選局の給電。電圧が機種の lnb に無ければ ErrUnsupported。
func (t *Tuner) SetLnb(voltage int) error {
	switch voltage {
	case 0, 11, 15:
	default:
		return backend.BadRequest("%s: 電圧は 0・11・15 のどれかにします (%d)", t.spec.Name, voltage)
	}
	if !t.def.SupportsLnb(voltage) {
		return &backend.StatusError{Code: 400, Msg: fmt.Sprintf("%s: この機材は %dv を出せません", t.spec.Name, voltage), Err: backend.ErrUnsupported}
	}
	t.mu.Lock()
	t.lnb = voltage
	t.mu.Unlock()
	return nil
}

// Close は PTX_STOP_STREAMING → PTX_DISABLE_LNB_POWER → close (13)。
func (t *Tuner) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopLocked()
	if t.f != nil {
		ioctl(t.f, ptxDisableLnbPower, 0)
		err := t.f.Close()
		t.f = nil
		return err
	}
	return nil
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// devReader は read() だけをする goroutine と、その結果を受ける Read。
// 復号や配信は別の goroutine に任せ、read() を止めない (4.3)。
type devReader struct {
	data chan []byte
	stop chan struct{}
	done chan struct{}
	err  atomic.Value // error
	left []byte
	once sync.Once
}

func newDevReader(f *os.File, last *atomic.Int64, wrap func(error) error) *devReader {
	r := &devReader{data: make(chan []byte, 64), stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(r.done)
		scratch := make([]byte, readSize) // 使い回す。列に入れるのは、読めたぶんだけの配列
		for {
			n, err := f.Read(scratch)
			if n > 0 {
				last.Store(time.Now().UnixNano())
				select {
				case r.data <- append(make([]byte, 0, n), scratch[:n]...):
				case <-r.stop:
					return
				}
			}
			if err != nil {
				select {
				case <-r.stop:
				default:
					r.err.Store(wrap(err))
				}
				return
			}
		}
	}()
	return r
}

func (r *devReader) Read(p []byte) (int, error) {
	if len(r.left) == 0 {
		select {
		case b := <-r.data:
			r.left = b
		case <-r.done:
			select { // 読み切ってから終わる
			case b := <-r.data:
				r.left = b
			default:
				if e, _ := r.err.Load().(error); e != nil {
					return 0, e
				}
				return 0, io.EOF
			}
		case <-r.stop:
			return 0, io.EOF
		}
	}
	n := copy(p, r.left)
	r.left = r.left[n:]
	return n, nil
}

func (r *devReader) Close() error { r.halt(); return nil }

func (r *devReader) halt() { r.once.Do(func() { close(r.stop) }) }

func (r *devReader) wait(d time.Duration) bool {
	select {
	case <-r.done:
		return true
	case <-time.After(d):
		return false
	}
}
