package cmdbackend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/channel"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/devices"
)

// firstReadSize は最初の TS を待つ 1 回の読みの大きさ。
const firstReadSize = 188 * 64

// tuner は cmd バックエンドのチューナー 1 つ。選局のたびに子プロセスを起動する。
type tuner struct {
	b      *Backend
	spec   backend.TunerSpec
	def    *devices.Def
	device string

	tuneMu sync.Mutex // Tune を重ねない
	mu     sync.Mutex // 以下を守る
	lnb    int
	cur    *session
	closed bool
}

var _ backend.Tuner = (*tuner)(nil)

func newTuner(b *Backend, spec backend.TunerSpec, def *devices.Def, device string) *tuner {
	return &tuner{b: b, spec: spec, def: def, device: device}
}

func (t *tuner) logf(format string, args ...any) {
	t.b.cfg.Log("[%s] %s", t.spec.Name, fmt.Sprintf(format, args...))
}

// DecryptedByDevice は機材の中で復号する機材か (6.4)。復号の経路を通さない。
func (t *tuner) DecryptedByDevice() bool { return t.def.DecryptedByDevice }

// Passthrough は FAKE_TUNE の機種。適合テストの TS は PSI を持たないので、復号を通さない。
func (t *tuner) Passthrough() bool { return t.def.Model == devices.ModelFake }

// SingleService は 1 サービスしか出ない機材か (6.4)。
func (t *tuner) SingleService() bool { return t.def.SingleService }

// Receivable はこの機材が受けられる種別。
func (t *tuner) Receivable() []string { return t.def.Receivable() }

// plan は選局の要求を検査して引数にする。機材に触る前の 400 はここで返す (7.2)。
func (t *tuner) plan(tn backend.Tuning) (typ string, args []string, err error) {
	name := t.spec.Name
	ch, perr := channel.Parse(tn.Channel)
	if perr != nil {
		return "", nil, backend.BadRequest("%s", perr.Error())
	}
	typ = tn.Type
	if typ == "" {
		typ = string(ch.Type)
	} else if typ != string(ch.Type) {
		return "", nil, backend.BadRequest("%s: %s は %s のチャンネルではありません", name, tn.Channel, typ)
	}
	if !t.def.CanReceive(typ) {
		return "", nil, backend.BadRequest("%s: %s は受けられません", name, typ)
	}
	if t.def.SingleService && tn.NeedsKey && tn.Service == 0 {
		return "", nil, backend.BadRequest("%s: この機材は観る局 (service) が要ります", name)
	}
	chArgs, ferr := FormatChannel(t.def.ChannelFormat, ch, len(t.def.TsidArg) > 0)
	if ferr != nil {
		var fe *FormatError
		if errors.As(ferr, &fe) {
			return "", nil, backend.BadRequest("%s: %s", name, fe.Error())
		}
		return "", nil, ferr
	}

	lnb := ""
	if ch.Type == channel.BS || ch.Type == channel.CS { // 衛星でない選局では給電を触らない
		t.mu.Lock()
		switch t.lnb {
		case 15:
			lnb = "15v"
		case 11:
			lnb = "11v"
		}
		t.mu.Unlock()
	}
	args = BuildArgs(t.def, Params{
		Device:  t.device,
		Type:    typ,
		Channel: chArgs,
		Lnb:     lnb,
		Service: tn.Service,
		TSID:    ch.TSID,
	})
	if len(args) == 0 {
		return "", nil, fmt.Errorf("%s: command が空です", name)
	}
	return typ, args, nil
}

// Tune は前の子プロセスを止めて、新しく起動する。最初の TS が届くか、
// 先に終わる (同期しなかった)、30 秒たつ、のどれかまで待つ。
func (t *tuner) Tune(tn backend.Tuning) error {
	t.tuneMu.Lock()
	defer t.tuneMu.Unlock()

	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return fmt.Errorf("%s: チューナーを閉じています", t.spec.Name)
	}

	typ, args, err := t.plan(tn)
	if err != nil {
		return err
	}
	_ = t.Stop()

	var s *session
	if t.def.IsHTTP() {
		t.logf("%s: HTTP で取りに行きます: %s", tn.Channel, args[0])
		s, err = t.startHTTPSession(args[0])
	} else {
		t.logf("%s: コマンドを起動します: %s", tn.Channel, strings.Join(args, " "))
		s, err = t.startSession(args)
	}
	if err != nil {
		return err
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		s.stop()
		return backend.ErrAborted
	}
	t.cur = s
	t.mu.Unlock()

	began := time.Now()
	if err := t.awaitFirst(s, typ); err != nil {
		t.mu.Lock()
		if t.cur == s {
			t.cur = nil
		}
		t.mu.Unlock()
		s.stop()
		return err
	}
	t.logf("%s: 最初の TS まで %.1f 秒", tn.Channel, time.Since(began).Seconds())
	return nil
}

func (t *tuner) startSession(args []string) (*session, error) {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = os.Environ()
	for k, v := range t.def.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	setProcAttr(cmd)
	cmd.WaitDelay = 2 * time.Second

	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("%s: パイプを作れません (%v)", t.spec.Name, err)
	}
	lw := &lineWriter{emit: func(line string) { t.logf("%s", line) }}
	cmd.Stdout = pw
	cmd.Stderr = lw
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return nil, fmt.Errorf("%s: コマンドを起動できません (%v)", t.spec.Name, err)
	}
	pw.Close() // 子だけが書く。子が終われば読みが EOF になる

	s := &session{
		cmd:     cmd,
		r:       pr,
		grace:   t.b.cfg.StopGrace,
		firstCh: make(chan firstResult, 1),
		done:    make(chan struct{}),
		abort:   make(chan struct{}),
	}
	go func() {
		err := cmd.Wait()
		lw.flush()
		s.waitErr = err
		close(s.done)
	}()
	go func() {
		buf := make([]byte, firstReadSize)
		n, err := pr.Read(buf)
		s.firstCh <- firstResult{data: buf[:n], err: err}
	}()
	return s, nil
}

func (t *tuner) awaitFirst(s *session, typ string) error {
	timer := time.NewTimer(t.b.cfg.FirstTSTimeout)
	defer timer.Stop()
	select {
	case r := <-s.firstCh:
		return t.gotFirst(s, r, typ)
	case <-s.done:
		// 書いてから終わったかもしれないので、少しだけ最初の読みを待つ
		select {
		case r := <-s.firstCh:
			return t.gotFirst(s, r, typ)
		case <-time.After(time.Second):
			return t.exited(s, typ)
		}
	case <-timer.C:
		t.logf("%s 秒待っても TS が届きませんでした", fmt.Sprint(int(t.b.cfg.FirstTSTimeout/time.Second)))
		return fmt.Errorf("%s: %w", t.spec.Name, backend.ErrNoTS)
	case <-s.abort:
		return backend.ErrAborted
	}
}

func (t *tuner) gotFirst(s *session, r firstResult, typ string) error {
	if len(r.data) > 0 {
		s.mu.Lock()
		s.first = r.data
		s.mu.Unlock()
		return nil
	}
	// 何も書かずに標準出力が閉じた。終了コードを見る
	select {
	case <-s.done:
	case <-time.After(t.b.cfg.StopGrace + 3*time.Second):
	case <-s.abort:
		return backend.ErrAborted
	}
	return t.exited(s, typ)
}

// exited は TS が来る前に子が終わったとき。busyExitCode なら 409、それ以外は同期しなかった。
func (t *tuner) exited(s *session, typ string) error {
	select {
	case <-s.done:
	default:
		return fmt.Errorf("%s: %w", t.spec.Name, backend.ErrNoLock)
	}
	if t.def.IsHTTP() {
		return t.httpExited(s, typ)
	}
	code := s.exitCode()
	t.logf("コマンドが TS を出さずに終わりました (終了コード %d)", code)
	if t.def.BusyExitCode != nil && code == *t.def.BusyExitCode {
		return &backend.StatusError{
			Code: 409,
			Msg:  fmt.Sprintf("%s のチューナーに空きがありません", typ),
			Err:  backend.ErrBusy,
		}
	}
	return fmt.Errorf("%s: %w", t.spec.Name, backend.ErrNoLock)
}

// Start は選局した TS の読み出しを返す。Close は読み出しを閉じるだけで、子プロセスは Stop で止める。
func (t *tuner) Start() (io.ReadCloser, error) {
	t.mu.Lock()
	s := t.cur
	t.mu.Unlock()
	if s == nil {
		return nil, fmt.Errorf("%s: 選局していません", t.spec.Name)
	}
	if !s.started.CompareAndSwap(false, true) {
		return nil, fmt.Errorf("%s: 読み出しは選局ごとに 1 つです", t.spec.Name)
	}
	return &tsReader{s: s}, nil
}

// Stop は子プロセスを止める (SIGTERM → 5 秒 → SIGKILL)。選局していなければ何もしない。
func (t *tuner) Stop() error {
	t.mu.Lock()
	s := t.cur
	t.cur = nil
	t.mu.Unlock()
	if s != nil {
		s.stop()
	}
	return nil
}

// Signal は出せない (C/N を持たない)。
func (t *tuner) Signal() (*backend.TunerSignal, error) { return nil, nil }

// SetLnb は次の選局の {lnb} に写すだけ。機種の lnb に無い電圧は断る。
func (t *tuner) SetLnb(voltage int) error {
	switch voltage {
	case 0, 11, 15:
	default:
		return backend.BadRequest("%s: 電圧は 0・11・15 のどれかにします (%d)", t.spec.Name, voltage)
	}
	if !t.def.SupportsLnb(voltage) {
		return &backend.StatusError{
			Code: 400,
			Msg:  fmt.Sprintf("%s: この機材は %dv を出せません", t.spec.Name, voltage),
			Err:  backend.ErrUnsupported,
		}
	}
	t.mu.Lock()
	t.lnb = voltage
	t.mu.Unlock()
	return nil
}

func (t *tuner) Close() error {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	return t.Stop()
}

// ---- 子プロセス 1 回ぶん ----

type firstResult struct {
	data []byte
	err  error
}

type session struct {
	cmd   *exec.Cmd          // HTTP のときは nil
	r     *os.File           // 子の標準出力 (HTTP のときは、本文を流すパイプ)
	grace time.Duration

	// HTTP のとき (startHTTPSession)。done を閉じる前に書く。
	cancel     context.CancelFunc
	httpStatus int    // 応答のステータス。応答が無ければ 0
	httpErr    error  // 応答が取れなかった、本文の途中で切れた
	httpBody   string // 200 でないときの本文の先頭 (記録に出す)

	firstCh chan firstResult
	done    chan struct{} // 子が終わったら閉じる。閉じる前に waitErr を書く
	waitErr error
	abort   chan struct{}

	abortOnce sync.Once
	started   atomic.Bool
	stopped   atomic.Bool

	mu    sync.Mutex
	first []byte // 最初の TS の塊。読み出しの先頭に返す
}

func (s *session) exitCode() int {
	if s.waitErr == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(s.waitErr, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// stop は子を止める。SIGTERM をグループごと送り、grace で終わらなければ SIGKILL。
func (s *session) stop() {
	s.stopped.Store(true)
	s.abortOnce.Do(func() { close(s.abort) })
	if s.cmd == nil { // HTTP。要求を取り消し、読み手を閉じて、書いている側を止める
		if s.cancel != nil {
			s.cancel()
		}
		s.r.Close()
		select {
		case <-s.done:
		case <-time.After(s.grace):
		}
		return
	}
	select {
	case <-s.done:
	default:
		terminateProc(s.cmd.Process)
		select {
		case <-s.done:
		case <-time.After(s.grace):
			killProc(s.cmd.Process)
			select {
			case <-s.done:
			case <-time.After(s.grace):
			}
		}
	}
	s.r.Close()
}

// tsReader は子の標準出力を TS として読む。子が非 0 で終わったら EOF ではなくその理由を返す。
type tsReader struct{ s *session }

func (r *tsReader) Read(p []byte) (int, error) {
	s := r.s
	s.mu.Lock()
	if len(s.first) > 0 {
		n := copy(p, s.first)
		s.first = s.first[n:]
		s.mu.Unlock()
		return n, nil
	}
	s.mu.Unlock()

	n, err := s.r.Read(p)
	if n > 0 || err == nil {
		return n, nil
	}
	if s.stopped.Load() {
		return 0, io.EOF
	}
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
	}
	if s.stopped.Load() {
		return 0, io.EOF
	}
	select {
	case <-s.done:
		if s.cmd == nil {
			if s.httpErr != nil {
				return 0, fmt.Errorf("HTTP の本文が途中で切れました (%v)", s.httpErr)
			}
		} else if code := s.exitCode(); code != 0 {
			return 0, fmt.Errorf("コマンドが終了しました (終了コード %d)", code)
		}
	default:
	}
	return 0, io.EOF
}

func (r *tsReader) Close() error { return nil }

// ---- 標準エラーを記録へ ----

// lineWriter は標準エラーを行に分けて emit に渡す。recpt1 の C/N は改行ではなく
// `\r` で上書きするので、`\r` も区切りにする。
type lineWriter struct {
	mu   sync.Mutex
	buf  []byte
	emit func(line string)
}

const maxLine = 4096

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexAny(w.buf, "\r\n")
		if i < 0 {
			break
		}
		w.line(w.buf[:i])
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > maxLine {
		w.line(w.buf)
		w.buf = nil
	}
	return len(p), nil
}

func (w *lineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.line(w.buf)
	w.buf = nil
}

func (w *lineWriter) line(b []byte) {
	if s := strings.TrimSpace(string(b)); s != "" {
		w.emit(s)
	}
}
