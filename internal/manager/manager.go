// Package manager は取り合い (仕様書 8 章)。チューナーの選び方・共有・取り上げ・引き継ぎ・余韻、
// 選局のあとの読み出しと利用者への配り方を持つ。バックエンドには依らない。
package manager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/channel"
)

const (
	DefaultGrace           = 5 * time.Second
	DefaultFirstTS         = 30 * time.Second
	DefaultSignalEvery     = 5 * time.Second
	DefaultSinkLimit       = 64 << 20
	readSize               = 188 * 816
	failedFor              = time.Hour
	maxRetriesPerSelection = 2
)

// ErrKilled は取り上げ・切断で接続を壊すときの理由。
var ErrKilled = errors.New("接続を壊します")

// ErrClientGone は利用者が読むのをやめたとき。
var ErrClientGone = errors.New("利用者が切りました")

// Processor は選局 1 つの TS を受けて、復号して返す (仕様書 9)。nil なら素通し。
type Processor interface {
	// Process は塊を受けて、出せるぶんを返す。パケットの境目は中で揃える。
	Process(in []byte) []byte
	// SetDecrypt は鍵の要る利用者がいるか。いなければカードに問い合わせない。
	SetDecrypt(on bool)
	// Close は選局が終わったとき。
	Close()
}

// ProcessorFactory は TS の選局ごとに復号を作る。nil を返せば素通し。
type ProcessorFactory func(tunerName string) Processor

// Config は Manager の設定。
type Config struct {
	Log backend.Logf
	// Open はチューナーの定義から機材を開く。
	Open func(spec backend.TunerSpec) (backend.Tuner, error)
	// NewProcessor は TS の選局ごとに復号を作る。nil か、nil を返せば素通し。
	NewProcessor ProcessorFactory
	// NewTLVProcessor は TLV (4K の MMT/TLV) の選局ごとに復号を作る。nil か、nil を返せば素通し。
	NewTLVProcessor ProcessorFactory
	// Notify は tuners の通知。
	Notify func(event string)

	Grace       time.Duration
	FirstTS     time.Duration
	SignalEvery time.Duration
	SinkLimit   int64
}

// Request は選局の要求 (8.3)。
type Request struct {
	Type     string
	Channel  string
	Priority int
	Use      string
	After    []string
	Pin      string
	Service  int

	// rangeErr は、チャンネル番号が範囲の外のときの理由 (Open が決める)。空なら範囲の中。
	// 範囲外の選局は、番号の範囲を見ない機種 (cmd) にだけ渡す。px4 の機材に渡すと、
	// 先に既存の利用者を取り上げてから 400 になってしまう
	rangeErr string
}

// Tuner はチューナー 1 つ。
type Tuner struct {
	spec    backend.TunerSpec
	be      backend.Tuner
	openErr string
	run     sync.Mutex // 選局の goroutine を 1 つずつにする
	lease   *Lease
	err     string
	failed  map[string]time.Time
	rank    *int
	group   string
}

// Lease は 1 回の選局。
type Lease struct {
	t       *Tuner
	typ     string
	channel string
	service int
	keyed   bool // 作ったときの利用者が鍵の要る利用者か
	single  bool

	ctx    context.Context
	cancel context.CancelFunc

	users      map[*Sink]struct{}
	ready      chan struct{}
	readyDone  bool
	err        error
	dead       bool
	tlv        bool
	graceTimer *time.Timer
	proc       Processor
	signal     *backend.TunerSignal
	lastData   time.Time
}

// Sink は利用者 1 つ (GET /denpa/stream の 1 接続)。
type Sink struct {
	Use      string
	Priority int
	keyed    bool
	q        *byteq
	lease    *Lease
	m        *Manager
	killMsg  string
	seq      uint64 // 繋いだ順 (/denpa/tuners の users の並び)
}

// Read は次の塊。正常に閉じたら io.EOF、壊すなら ErrKilled。
func (s *Sink) Read() ([]byte, error) { return s.q.pop() }

// TLV は選局が TLV か (Content-Type を決める)。
func (s *Sink) TLV() bool {
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	return s.lease != nil && s.lease.tlv
}

// Reason は壊されたときの理由。
func (s *Sink) Reason() string {
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	return s.killMsg
}

// Release は利用者が切ったとき。
func (s *Sink) Release() { s.m.release(s) }

// Manager は取り合いの本体。
type Manager struct {
	cfg      Config
	mu       sync.Mutex
	tuners   []*Tuner
	groups   []*group
	detected bool
	closed   bool
	sinkSeq  uint64 // join のたびに進める (m.mu で守る)
}

type group struct {
	key     string
	types   []string
	members []*Tuner
}

// New は Manager を作る。
func New(cfg Config) *Manager {
	if cfg.Log == nil {
		cfg.Log = func(string, ...any) {}
	}
	if cfg.Notify == nil {
		cfg.Notify = func(string) {}
	}
	if cfg.Grace <= 0 {
		cfg.Grace = DefaultGrace
	}
	if cfg.FirstTS <= 0 {
		cfg.FirstTS = DefaultFirstTS
	}
	if cfg.SignalEvery <= 0 {
		cfg.SignalEvery = DefaultSignalEvery
	}
	if cfg.SinkLimit <= 0 {
		cfg.SinkLimit = DefaultSinkLimit
	}
	return &Manager{cfg: cfg}
}

func (m *Manager) logf(t *Tuner, format string, args ...any) {
	m.cfg.Log("[%s] %s", t.spec.Name, fmt.Sprintf(format, args...))
}

func keyless(use string) bool {
	f := strings.Fields(use)
	return len(f) > 0 && (f[0] == "epg" || f[0] == "logo" || f[0] == "scan")
}

func isScan(use string) bool {
	f := strings.Fields(use)
	return len(f) > 0 && f[0] == "scan"
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func groupKey(types []string) string {
	c := append([]string(nil), types...)
	sort.Strings(c)
	return strings.Join(c, ",")
}

func (t *Tuner) pin() string {
	if t.spec.Device != "" {
		return t.spec.Device
	}
	return t.spec.Name
}

func (t *Tuner) receivable() []string {
	if r, ok := t.be.(backend.Receivabler); ok {
		return r.Receivable()
	}
	return nil
}

func (t *Tuner) eligible(typ string) bool {
	return !t.spec.Disabled && t.be != nil && contains(t.spec.Types, typ) && contains(t.receivable(), typ)
}

// laxChannels は番号の範囲を見ずに選局コマンドに任せる機種か (cmd)。
func (t *Tuner) laxChannels() bool {
	l, ok := t.be.(backend.LaxChannelser)
	return ok && l.LaxChannels()
}

// eligibleFor は eligible に加えて、範囲外のチャンネルなら範囲を見ない機種だけを通す。
func (t *Tuner) eligibleFor(req Request) bool {
	return t.eligible(req.Type) && (req.rangeErr == "" || t.laxChannels())
}

func (t *Tuner) isSingle() bool {
	s, ok := t.be.(backend.SingleServicer)
	return ok && s.SingleService()
}

func (t *Tuner) decryptedByDevice() bool {
	d, ok := t.be.(backend.DecryptedByDevicer)
	return ok && d.DecryptedByDevice()
}

// noProcess は復号の経路を通さない機材 (機材の中で復号する・FAKE_TUNE)。
func (t *Tuner) noProcess() bool {
	if p, ok := t.be.(backend.Passthroughers); ok && p.Passthrough() {
		return true
	}
	return t.decryptedByDevice()
}

func (l *Lease) matches(req Request, canonical string) bool {
	if l.dead || l.typ != req.Type || l.channel != canonical {
		return false
	}
	if !l.single || keyless(req.Use) {
		return true
	}
	return l.service != 0 && l.service == req.Service
}

// maxPrio は after にいない利用者の最大の優先度。いなければ ok=false。
func (l *Lease) maxPrio(after map[string]bool) (int, bool) {
	best, ok := 0, false
	for s := range l.users {
		if after[s.Use] {
			continue
		}
		if !ok || s.Priority > best {
			best, ok = s.Priority, true
		}
	}
	return best, ok
}

// usable は自分 (優先度 prio) が使ってよいチューナーか (8.3)。
func (l *Lease) usable(prio int, after map[string]bool) bool {
	if l == nil || l.dead {
		return true
	}
	p, ok := l.maxPrio(after)
	return !ok || p < prio
}

// ---- 定義 ----

// SetSpecs は定義を入れ替える。名前が同じチューナーは選局を保つ。
func (m *Manager) SetSpecs(specs []backend.TunerSpec, detected bool) {
	m.mu.Lock()
	old := map[string]*Tuner{}
	for _, t := range m.tuners {
		old[t.spec.Name] = t
	}
	var next []*Tuner
	var toClose []backend.Tuner
	for _, s := range specs {
		if t := old[s.Name]; t != nil && t.spec.Device == s.Device && t.openErr == "" {
			delete(old, s.Name)
			if s.Disabled && t.lease != nil {
				m.endLease(t.lease, "無効にされました", false)
			}
			t.spec = s
			t.failed = map[string]time.Time{}
			next = append(next, t)
			continue
		}
		nt := &Tuner{spec: s, failed: map[string]time.Time{}}
		if t := old[s.Name]; t != nil { // device が変わった
			delete(old, s.Name)
			if t.lease != nil {
				m.endLease(t.lease, "定義が変わりました", false)
			}
			if t.be != nil {
				toClose = append(toClose, t.be)
			}
		}
		if m.cfg.Open != nil {
			be, err := m.cfg.Open(s)
			if err != nil {
				nt.openErr = err.Error()
				nt.err = err.Error()
				m.cfg.Log("[%s] 開けません: %v", s.Name, err)
			} else {
				nt.be = be
			}
		}
		next = append(next, nt)
	}
	for _, t := range old {
		if t.lease != nil {
			m.endLease(t.lease, "チューナーを手放しました", false)
		}
		if t.be != nil {
			toClose = append(toClose, t.be)
		}
	}
	m.tuners = next
	m.detected = detected
	m.regroup()
	m.mu.Unlock()
	for _, be := range toClose {
		be.Close()
	}
	m.cfg.Notify("tuners")
}

// Specs は今の定義。
func (m *Manager) Specs() []backend.TunerSpec {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]backend.TunerSpec, 0, len(m.tuners))
	for _, t := range m.tuners {
		out = append(out, t.spec)
	}
	return out
}

// Detected は定義が無く自分で見つけた状態か。
func (m *Manager) Detected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.detected
}

// regroup は組と rank を付ける (8.2)。m.mu を持って呼ぶ。
func (m *Manager) regroup() {
	m.groups = nil
	byKey := map[string]*group{}
	for _, t := range m.tuners {
		t.rank = nil
		if t.spec.Disabled {
			continue
		}
		k := groupKey(t.spec.Types)
		t.group = k
		g := byKey[k]
		if g == nil {
			g = &group{key: k, types: append([]string(nil), t.spec.Types...)}
			byKey[k] = g
			m.groups = append(m.groups, g)
		}
		g.members = append(g.members, t)
	}
	sort.SliceStable(m.groups, func(i, j int) bool { return len(m.groups[i].types) < len(m.groups[j].types) })
	for i, g := range m.groups {
		for _, t := range g.members {
			r := i
			t.rank = &r
		}
	}
}

// ---- 選局 ----

func statusErr(code int, format string, args ...any) error {
	return &backend.StatusError{Code: code, Msg: fmt.Sprintf(format, args...)}
}

func busyErr(typ string) error {
	return &backend.StatusError{Code: 409, Msg: typ + " のチューナーに空きがありません", Err: backend.ErrBusy}
}

func noLockErr(names []string, reason string) error {
	return &backend.NoLockError{Name: strings.Join(names, "・"), Reason: reason}
}

// noLockReason は同期しなかった失敗に添えられた理由 (選局コマンドの標準エラー)。
func noLockReason(err error) string {
	var nl *backend.NoLockError
	if errors.As(err, &nl) {
		return nl.Reason
	}
	return ""
}

// Open は選局して、最初の TS が届くまで待つ。
func (m *Manager) Open(req Request) (*Sink, error) {
	if req.Type == "" || req.Channel == "" {
		return nil, statusErr(400, "type と channel が必要です")
	}
	// 番号の範囲は、機種ごとに見る (px4 の機材は選局の値を計算するので 400、cmd は選局コマンドに任せる)
	ch, err := channel.ParseLax(req.Channel)
	if err != nil {
		return nil, statusErr(400, "%s", err.Error())
	}
	if string(ch.Type) != req.Type {
		return nil, statusErr(400, "%s: type と channel が合いません", req.Channel)
	}
	req.Channel = ch.String()
	// 厳密な Parse が断る範囲外は、範囲を見ない機種 (cmd) にだけ渡す。候補が無ければ、ここで 400
	if _, serr := channel.Parse(req.Channel); serr != nil {
		req.rangeErr = serr.Error()
		if !m.anyLax(req.Type) {
			return nil, statusErr(400, "%s", req.rangeErr)
		}
	}
	if req.Use == "" {
		req.Use = "不明"
	}

	var tried []string
	onlyGroup := ""
	reason := "" // 最後に同期しなかった選局の理由
	for {
		sink, l, t, err := m.acquire(req, tried, onlyGroup)
		if err != nil {
			if len(tried) > 0 {
				return nil, noLockErr(tried, reason)
			}
			return nil, err
		}
		<-l.ready
		if l.err == nil {
			return sink, nil
		}
		if !errors.Is(l.err, backend.ErrNoLock) {
			return nil, l.err
		}
		tried = append(tried, t.spec.Name)
		reason = noLockReason(l.err)
		if req.Pin != "" || isScan(req.Use) || len(tried) >= maxRetriesPerSelection {
			return nil, noLockErr(tried, reason)
		}
		m.cfg.Log("[%s] %s: 同期しなかったので同じ組の別のチューナーで選局し直します", t.spec.Name, req.Channel)
		m.mu.Lock()
		onlyGroup = t.group
		m.mu.Unlock()
	}
}

// anyLax は、この種別を受けられて、範囲を見ない (cmd) チューナーがあるか。
func (m *Manager) anyLax(typ string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tuners {
		if t.eligible(typ) && t.laxChannels() {
			return true
		}
	}
	return false
}

func (m *Manager) acquire(req Request, exclude []string, onlyGroup string) (*Sink, *Lease, *Tuner, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, nil, nil, statusErr(503, "エージェントを止めています")
	}
	after := map[string]bool{}
	for _, a := range req.After {
		after[a] = true
	}

	if req.Pin != "" {
		var t *Tuner
		for _, c := range m.tuners {
			if c.pin() == req.Pin || c.spec.Name == req.Pin {
				t = c
				break
			}
		}
		if t == nil {
			return nil, nil, nil, statusErr(400, "名指しのチューナーがありません (%s)", req.Pin)
		}
		if t.spec.Disabled {
			return nil, nil, nil, statusErr(400, "%s: 無効なチューナーです", t.spec.Name)
		}
		if !t.eligible(req.Type) {
			return nil, nil, nil, statusErr(400, "%s: %s は受けられません", t.spec.Name, req.Type)
		}
		if req.rangeErr != "" && !t.laxChannels() {
			return nil, nil, nil, statusErr(400, "%s", req.rangeErr)
		}
		if l := t.lease; l != nil && !l.dead && l.matches(req, req.Channel) {
			return m.join(l, req), l, t, nil
		}
		if !t.lease.usable(req.Priority, after) {
			return nil, nil, nil, busyErr(req.Type)
		}
		s, l := m.begin(t, req, after)
		return s, l, t, nil
	}

	// 同じ種別・同じチャンネルの選局を共有する (優先度が同じか高い相手がいるとき)
	for _, t := range m.tuners {
		l := t.lease
		if l == nil || !t.eligibleFor(req) || !l.matches(req, req.Channel) {
			continue
		}
		if p, ok := l.maxPrio(after); ok && p >= req.Priority {
			return m.join(l, req), l, t, nil
		}
	}

	chKey := req.Type + "/" + req.Channel
	for _, g := range m.groups {
		if onlyGroup != "" && g.key != onlyGroup {
			continue
		}
		if !contains(g.types, req.Type) {
			continue
		}
		var members, back []*Tuner
		for _, t := range g.members {
			if contains(exclude, t.spec.Name) || !t.eligibleFor(req) {
				continue
			}
			if at, bad := t.failed[chKey]; bad && time.Since(at) < failedFor {
				back = append(back, t)
			} else {
				members = append(members, t)
			}
		}
		members = append(members, back...)

		var same, grace, free, victim *Tuner
		victimPrio := 0
		for _, t := range members {
			l := t.lease
			if l != nil && l.dead {
				l = nil
			}
			if !l.usable(req.Priority, after) {
				continue
			}
			switch {
			case l == nil:
				if free == nil {
					free = t
				}
			case l.matches(req, req.Channel):
				if same == nil {
					same = t
				}
			case len(l.users) == 0:
				if grace == nil {
					grace = t
				}
			default:
				p, _ := l.maxPrio(after)
				if victim == nil || p < victimPrio {
					victim, victimPrio = t, p
				}
			}
		}
		switch {
		case same != nil:
			return m.join(same.lease, req), same.lease, same, nil
		case grace != nil:
			s, l := m.begin(grace, req, after)
			return s, l, grace, nil
		case free != nil:
			s, l := m.begin(free, req, after)
			return s, l, free, nil
		case victim != nil:
			s, l := m.begin(victim, req, after)
			return s, l, victim, nil
		}
	}
	return nil, nil, nil, busyErr(req.Type)
}

// join は選局に利用者を足す。m.mu を持って呼ぶ。
func (m *Manager) join(l *Lease, req Request) *Sink {
	s := &Sink{Use: req.Use, Priority: req.Priority, keyed: !keyless(req.Use), q: newByteq(m.cfg.SinkLimit), lease: l, m: m}
	m.sinkSeq++
	s.seq = m.sinkSeq
	l.users[s] = struct{}{}
	if l.graceTimer != nil {
		l.graceTimer.Stop()
		l.graceTimer = nil
	}
	m.updateDecrypt(l)
	m.cfg.Notify("tuners")
	return s
}

// begin はチューナーに新しい選局を作る。前の選局は取り上げる。m.mu を持って呼ぶ。
func (m *Manager) begin(t *Tuner, req Request, after map[string]bool) (*Sink, *Lease) {
	if old := t.lease; old != nil {
		m.cfg.Log("[%s] %s: %s が %s のチューナーを取り上げます", t.spec.Name, old.channel, req.Use, old.channel)
		m.evict(old, req.Use, after)
	}
	ctx, cancel := context.WithCancel(context.Background())
	l := &Lease{
		t: t, typ: req.Type, channel: req.Channel, keyed: !keyless(req.Use), single: t.isSingle(),
		ctx: ctx, cancel: cancel, users: map[*Sink]struct{}{}, ready: make(chan struct{}),
	}
	if l.single && l.keyed {
		l.service = req.Service
	}
	t.lease = l
	s := m.join(l, req)
	go m.run(l, req.Use)
	return s, l
}

// evict は取り上げられた選局の利用者を片付ける。after の利用者は正常に閉じる。
func (m *Manager) evict(l *Lease, byUse string, after map[string]bool) {
	reason := fmt.Sprintf("優先度の高い要求 (%s) にチューナーを譲りました", byUse)
	m.finishLease(l, &backend.StatusError{Code: 409, Msg: reason}, reason, after)
}

// endLease は選局を畳む。graceful なら利用者を正常に閉じる。m.mu を持って呼ぶ。
func (m *Manager) endLease(l *Lease, reason string, graceful bool) {
	var all map[string]bool
	if graceful {
		all = map[string]bool{}
		for s := range l.users {
			all[s.Use] = true
		}
	}
	m.finishLease(l, &backend.StatusError{Code: 409, Msg: reason}, reason, all)
}

func (m *Manager) finishLease(l *Lease, err error, reason string, graceful map[string]bool) {
	if l.dead {
		return
	}
	l.dead = true
	if l.graceTimer != nil {
		l.graceTimer.Stop()
	}
	m.setReady(l, err)
	for s := range l.users {
		if graceful[s.Use] {
			s.q.finish(nil, true)
		} else {
			s.killMsg = reason
			s.q.finish(ErrKilled, false)
		}
	}
	l.users = map[*Sink]struct{}{}
	if l.t.lease == l {
		l.t.lease = nil
	}
	l.cancel()
	m.cfg.Notify("tuners")
}

// setReady は選局の結果を決める。m.mu を持って呼ぶ。
func (m *Manager) setReady(l *Lease, err error) {
	if l.readyDone {
		return
	}
	l.readyDone = true
	l.err = err
	close(l.ready)
}

func (m *Manager) updateDecrypt(l *Lease) {
	if l.proc == nil {
		return
	}
	on := false
	for s := range l.users {
		if s.keyed {
			on = true
		}
	}
	l.proc.SetDecrypt(on)
}

func (m *Manager) release(s *Sink) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := s.lease
	s.q.finish(ErrClientGone, false)
	if l == nil {
		return
	}
	if _, ok := l.users[s]; !ok {
		return
	}
	delete(l.users, s)
	m.updateDecrypt(l)
	m.cfg.Notify("tuners")
	if len(l.users) == 0 && !l.dead {
		l.graceTimer = time.AfterFunc(m.cfg.Grace, func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if len(l.users) == 0 && !l.dead {
				l.dead = true
				m.setReady(l, statusErr(409, "選局を終えました"))
				if l.t.lease == l {
					l.t.lease = nil
				}
				l.cancel()
				m.cfg.Notify("tuners")
			}
		})
	}
}

// ---- 選局の goroutine ----

func (m *Manager) failLease(l *Lease, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l.dead {
		return
	}
	t := l.t
	if errors.Is(err, backend.ErrNoLock) || errors.Is(err, backend.ErrNoTS) {
		t.failed[l.typ+"/"+l.channel] = time.Now()
	}
	var se *backend.StatusError
	if !errors.As(err, &se) || se.Code != 409 {
		t.err = err.Error()
	}
	m.cfg.Log("[%s] %s: 選局に失敗しました: %v", t.spec.Name, l.channel, err)
	m.finishLease(l, err, err.Error(), nil)
}

func (m *Manager) run(l *Lease, firstUse string) {
	t := l.t
	t.run.Lock()
	defer t.run.Unlock()
	if l.ctx.Err() != nil {
		return
	}
	be := t.be
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-l.ctx.Done():
			be.Stop()
		case <-done:
		}
	}()

	began := time.Now()
	if err := be.Tune(backend.Tuning{Type: l.typ, Channel: l.channel, Service: l.service, NeedsKey: l.keyed}); err != nil {
		m.failLease(l, err)
		be.Stop()
		return
	}
	if l.ctx.Err() != nil {
		be.Stop()
		return
	}
	rd, err := be.Start()
	if err != nil {
		m.failLease(l, fmt.Errorf("%s: %w", t.spec.Name, err))
		be.Stop()
		return
	}
	defer rd.Close()

	// 最初の TS を待つ (8.4)
	watch := time.AfterFunc(m.cfg.FirstTS, func() {
		m.mu.Lock()
		ready := l.readyDone
		m.mu.Unlock()
		if !ready {
			m.failLease(l, fmt.Errorf("%s: %w", t.spec.Name, backend.ErrNoTS))
		}
	})
	defer watch.Stop()

	go m.pollSignal(l, done)
	err = m.pump(l, rd, began)
	be.Stop()
	if err != nil && l.ctx.Err() == nil {
		if errors.Is(err, backend.ErrUnplugged) {
			err = fmt.Errorf("%s: %w", t.spec.Name, backend.ErrUnplugged)
		} else if errors.Is(err, io.EOF) {
			err = fmt.Errorf("%s: 受信が止まりました", t.spec.Name)
		} else {
			err = fmt.Errorf("%s: 受信が止まりました (%v)", t.spec.Name, err)
		}
		m.failLease(l, err)
	}
	m.mu.Lock()
	if l.proc != nil {
		go l.proc.Close()
	}
	m.mu.Unlock()
}

func (m *Manager) pump(l *Lease, rd io.Reader, began time.Time) error {
	var (
		first = true
		dq    *byteq
		proc  Processor
	)
	// 読むための配列は 1 つを使い回し、読めたぶんだけを新しい配列に写す。
	// 読むたびに大きな配列を確保して buf[:n] を列に入れると、数 KB の読みでも 150KB の配列を抱え込み、
	// 列の上限 (バイト数) の何十倍ものメモリを使い、確保とクリアで CPU も尽きる (実機の nasne で 15 秒に 2.9MB しか流れなかった)。
	scratch := make([]byte, readSize)
	for {
		n, err := rd.Read(scratch)
		if n > 0 {
			chunk := append(make([]byte, 0, n), scratch[:n]...)
			if first {
				first = false
				m.mu.Lock()
				l.tlv = chunk[0] == 0x7F
				if l.tlv {
					if m.cfg.NewTLVProcessor != nil && !l.t.noProcess() {
						proc = m.cfg.NewTLVProcessor(l.t.spec.Name)
					}
				} else if m.cfg.NewProcessor != nil && !l.t.noProcess() {
					proc = m.cfg.NewProcessor(l.t.spec.Name)
				}
				if proc != nil {
					l.proc = proc
					m.updateDecrypt(l)
					dq = newByteq(m.cfg.SinkLimit)
					go m.decodeLoop(l, proc, dq)
				}
				l.lastData = time.Now()
				l.t.err = ""
				delete(l.t.failed, l.typ+"/"+l.channel)
				m.setReady(l, nil)
				m.mu.Unlock()
				m.logf(l.t, "%s: 最初の TS まで %.1f 秒", l.channel, time.Since(began).Seconds())
				m.cfg.Notify("tuners")
			}
			m.mu.Lock()
			l.lastData = time.Now()
			m.mu.Unlock()
			if dq != nil {
				if !dq.push(chunk) {
					m.logf(l.t, "復号が追いつかないため、読んだぶんを捨てました")
				}
			} else {
				m.deliver(l, chunk)
			}
		}
		if err != nil {
			if dq != nil {
				dq.finish(nil, true)
			}
			return err
		}
	}
}

func (m *Manager) decodeLoop(l *Lease, proc Processor, q *byteq) {
	for {
		b, err := q.pop()
		if err != nil {
			return
		}
		if out := proc.Process(b); len(out) > 0 {
			m.deliver(l, out)
		}
	}
}

// deliver は利用者全員に配る。溜め込み過ぎた利用者だけ切る (8.4)。
func (m *Manager) deliver(l *Lease, b []byte) {
	m.mu.Lock()
	sinks := make([]*Sink, 0, len(l.users))
	for s := range l.users {
		sinks = append(sinks, s)
	}
	m.mu.Unlock()
	for _, s := range sinks {
		if !s.q.push(b) {
			m.mu.Lock()
			if _, ok := l.users[s]; ok {
				s.killMsg = "読み出しが追いつかないため切断しました"
				s.q.finish(ErrKilled, false)
				delete(l.users, s)
				m.updateDecrypt(l)
				m.cfg.Log("[%s] %s: %s (%s)", l.t.spec.Name, l.channel, s.killMsg, s.Use)
			}
			m.mu.Unlock()
		}
	}
}

func (m *Manager) pollSignal(l *Lease, done <-chan struct{}) {
	tk := time.NewTicker(m.cfg.SignalEvery)
	defer tk.Stop()
	for {
		select {
		case <-done:
			return
		case <-l.ctx.Done():
			return
		case <-tk.C:
			sig, err := l.t.be.Signal()
			m.mu.Lock()
			if err == nil {
				l.signal = sig
			}
			m.mu.Unlock()
		}
	}
}

// ---- 一覧 ----

// UserView は users の 1 行。
type UserView struct {
	Use      string `json:"use"`
	Priority int    `json:"priority"`
}

// ChannelView は channel。
type ChannelView struct {
	Type    string `json:"type"`
	Channel string `json:"channel"`
}

// TunerView は GET /denpa/tuners の 1 行 (10.3)。
type TunerView struct {
	Index      int                  `json:"index"`
	Rank       *int                 `json:"rank"`
	Name       string               `json:"name"`
	Types      []string             `json:"types"`
	Receivable []string             `json:"receivable"`
	Disabled   bool                 `json:"disabled"`
	Device     string               `json:"device"`
	Pin        string               `json:"pin"`
	Lnb        *string              `json:"lnb"`
	Channel    *ChannelView         `json:"channel"`
	Users      []UserView           `json:"users"`
	Error      *string              `json:"error"`
	Signal     *backend.TunerSignal `json:"signal"`
}

// Snapshot は一覧。
func (m *Manager) Snapshot() []TunerView {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]TunerView, 0, len(m.tuners))
	for i, t := range m.tuners {
		v := TunerView{
			Index: i, Rank: t.rank, Name: t.spec.Name, Types: append([]string{}, t.spec.Types...),
			Disabled: t.spec.Disabled, Device: t.spec.Device, Pin: t.pin(), Users: []UserView{},
		}
		if t.be != nil {
			if r := t.receivable(); r != nil {
				v.Receivable = append([]string{}, r...)
			}
		}
		if t.spec.Lnb != "" {
			lnb := t.spec.Lnb
			v.Lnb = &lnb
		}
		if t.err != "" {
			e := t.err
			v.Error = &e
		}
		if l := t.lease; l != nil && !l.dead {
			v.Channel = &ChannelView{Type: l.typ, Channel: l.channel}
			ordered := make([]*Sink, 0, len(l.users))
			for s := range l.users {
				ordered = append(ordered, s)
			}
			sort.Slice(ordered, func(a, b int) bool { return ordered[a].seq < ordered[b].seq })
			for _, s := range ordered {
				v.Users = append(v.Users, UserView{Use: s.Use, Priority: s.Priority})
			}
			if len(l.users) > 0 && l.signal != nil {
				sig := *l.signal
				v.Signal = &sig
			}
		}
		out = append(out, v)
	}
	return out
}

// ActiveCards は復号中のチューナーの名前 (/denpa/card の tuners)。
func (m *Manager) ActiveCards() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, t := range m.tuners {
		if l := t.lease; l != nil && !l.dead && l.proc != nil && len(l.users) > 0 {
			for s := range l.users {
				if s.keyed {
					out = append(out, t.spec.Name)
					break
				}
			}
		}
	}
	return out
}

// DecryptedByDeviceTuners は機材の中で復号するチューナー (/denpa/card の擬似リーダー用)。
func (m *Manager) DecryptedByDeviceTuners() (models map[string][]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	models = map[string][]string{}
	for _, t := range m.tuners {
		if t.be != nil && t.decryptedByDevice() {
			var active []string
			if l := t.lease; l != nil && !l.dead && len(l.users) > 0 {
				active = append(active, t.spec.Name)
			}
			models[t.spec.Name] = active
		}
	}
	return models
}

// Recording は rec の利用者がいるか (止まり方 13)。
func (m *Manager) Recording() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tuners {
		if l := t.lease; l != nil && !l.dead {
			for s := range l.users {
				if f := strings.Fields(s.Use); len(f) > 0 && f[0] == "rec" {
					return true
				}
			}
		}
	}
	return false
}

// Shutdown は選局を全部離し、チューナーを閉じる。
func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.closed = true
	var bes []backend.Tuner
	for _, t := range m.tuners {
		if t.lease != nil {
			m.endLease(t.lease, "エージェントを止めます", false)
		}
		if t.be != nil {
			bes = append(bes, t.be)
		}
	}
	m.mu.Unlock()
	for _, be := range bes {
		be.Close()
	}
}
