package manager

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
)

// fakeTuner は選局すると 188 バイトのパケットを流し続ける偽の機材。
type fakeTuner struct {
	name   string
	types  []string
	noLock atomic.Bool
	tunes  atomic.Int32
	single bool

	mu     sync.Mutex
	stopCh chan struct{}
	last   backend.Tuning
}

func (f *fakeTuner) Receivable() []string { return f.types }
func (f *fakeTuner) SingleService() bool  { return f.single }

func (f *fakeTuner) Tune(t backend.Tuning) error {
	f.tunes.Add(1)
	if f.noLock.Load() {
		return fmt.Errorf("%s: %w", f.name, backend.ErrNoLock)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last = t
	f.stopCh = make(chan struct{})
	return nil
}

type fakeReader struct {
	stop <-chan struct{}
}

func (r *fakeReader) Read(p []byte) (int, error) {
	select {
	case <-r.stop:
		return 0, io.EOF
	case <-time.After(2 * time.Millisecond):
	}
	n := 188
	if n > len(p) {
		n = len(p)
	}
	for i := 0; i < n; i++ {
		p[i] = 0
	}
	p[0] = 0x47
	return n, nil
}
func (r *fakeReader) Close() error { return nil }

func (f *fakeTuner) Start() (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &fakeReader{stop: f.stopCh}, nil
}

func (f *fakeTuner) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopCh != nil {
		select {
		case <-f.stopCh:
		default:
			close(f.stopCh)
		}
	}
	return nil
}
func (f *fakeTuner) Signal() (*backend.TunerSignal, error) { return nil, nil }
func (f *fakeTuner) SetLnb(int) error                      { return nil }
func (f *fakeTuner) Close() error                          { return f.Stop() }

type rig struct {
	m      *Manager
	tuners map[string]*fakeTuner
	logs   []string
	mu     sync.Mutex
}

func newRig(t *testing.T, specs ...backend.TunerSpec) *rig {
	t.Helper()
	r := &rig{tuners: map[string]*fakeTuner{}}
	r.m = New(Config{
		Log: func(f string, a ...any) {
			r.mu.Lock()
			r.logs = append(r.logs, fmt.Sprintf(f, a...))
			r.mu.Unlock()
		},
		Open: func(spec backend.TunerSpec) (backend.Tuner, error) {
			ft := &fakeTuner{name: spec.Name, types: spec.Types}
			r.tuners[spec.Name] = ft
			return ft, nil
		},
		Grace:   150 * time.Millisecond,
		FirstTS: 2 * time.Second,
	})
	r.m.SetSpecs(specs, false)
	t.Cleanup(r.m.Shutdown)
	return r
}

func spec(name string, types ...string) backend.TunerSpec {
	return backend.TunerSpec{Name: name, Types: types, Device: "cmd:x:" + name}
}

func open(t *testing.T, m *Manager, typ, ch string, prio int, use string, after ...string) *Sink {
	t.Helper()
	s, err := m.Open(Request{Type: typ, Channel: ch, Priority: prio, Use: use, After: after})
	if err != nil {
		t.Fatalf("Open(%s %s %s): %v", typ, ch, use, err)
	}
	return s
}

func openErr(m *Manager, typ, ch string, prio int, use string) error {
	_, err := m.Open(Request{Type: typ, Channel: ch, Priority: prio, Use: use})
	return err
}

func readOne(t *testing.T, s *Sink) []byte {
	t.Helper()
	done := make(chan struct{})
	var b []byte
	var err error
	go func() { b, err = s.Read(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("TS が来ない")
	}
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return b
}

func readErr(t *testing.T, s *Sink) error {
	t.Helper()
	for i := 0; i < 100000; i++ {
		done := make(chan error, 1)
		go func() { _, err := s.Read(); done <- err }()
		select {
		case err := <-done:
			if err != nil {
				return err
			}
		case <-time.After(3 * time.Second):
			t.Fatal("終わらない")
		}
	}
	return nil
}

func channelOf(r *rig, name string) string {
	for _, v := range r.m.Snapshot() {
		if v.Name == name && v.Channel != nil {
			return v.Channel.Channel
		}
	}
	return ""
}

func TestOpen_選局して流れる_利用者が切れても余韻を過ぎれば選局が消える(t *testing.T) {
	r := newRig(t, spec("A", "GR"))
	s := open(t, r.m, "GR", "T27", 10, "rec 1")
	if b := readOne(t, s); len(b) == 0 || b[0] != 0x47 {
		t.Fatalf("TS ではない: %v", b[:1])
	}
	if got := channelOf(r, "A"); got != "T27" {
		t.Fatalf("channel=%q", got)
	}
	s.Release()
	if channelOf(r, "A") != "T27" {
		t.Errorf("余韻の間は選局が残る")
	}
	time.Sleep(400 * time.Millisecond)
	if got := channelOf(r, "A"); got != "" {
		t.Errorf("余韻のあとは消える: %q", got)
	}
}

func TestOpen_余韻の間に同じチャンネルなら選局し直さない(t *testing.T) {
	r := newRig(t, spec("A", "GR"))
	s := open(t, r.m, "GR", "T27", 10, "rec 1")
	readOne(t, s)
	s.Release()
	s2 := open(t, r.m, "GR", "T27", 10, "rec 2")
	readOne(t, s2)
	if n := r.tuners["A"].tunes.Load(); n != 1 {
		t.Errorf("Tune は 1 回のはず: %d", n)
	}
}

func TestOpen_同じチャンネルは共有する(t *testing.T) {
	r := newRig(t, spec("A", "GR"), spec("B", "GR"))
	s1 := open(t, r.m, "GR", "T27", 10, "rec 1")
	s2 := open(t, r.m, "GR", "T27", 9, "live GR/T27")
	readOne(t, s1)
	readOne(t, s2)
	if r.tuners["A"].tunes.Load()+r.tuners["B"].tunes.Load() != 1 {
		t.Errorf("選局は 1 回のはず")
	}
	snap := r.m.Snapshot()
	if len(snap[0].Users) != 2 {
		t.Errorf("利用者は 2 人: %+v", snap[0].Users)
	}
}

func TestOpen_優先度が低い利用者は取り上げられ_接続を壊される(t *testing.T) {
	r := newRig(t, spec("A", "GR"))
	low := open(t, r.m, "GR", "T27", 3, "epg GR/T27")
	readOne(t, low)
	high := open(t, r.m, "GR", "T21", 10, "rec 1")
	readOne(t, high)
	if err := readErr(t, low); !errors.Is(err, ErrKilled) {
		t.Fatalf("壊されるはず: %v", err)
	}
	if got := low.Reason(); !strings.Contains(got, "優先度の高い要求 (rec 1)") {
		t.Errorf("理由: %q", got)
	}
	if channelOf(r, "A") != "T21" {
		t.Errorf("選局が替わっていない")
	}
}

func TestOpen_同じ優先度の相手は取り上げず409(t *testing.T) {
	r := newRig(t, spec("A", "GR"))
	s := open(t, r.m, "GR", "T27", 10, "rec 1")
	readOne(t, s)
	err := openErr(r.m, "GR", "T21", 10, "rec 2")
	if backend.StatusOf(err) != 409 || err.Error() != "GR のチューナーに空きがありません" {
		t.Fatalf("got %v", err)
	}
	if channelOf(r, "A") != "T27" {
		t.Errorf("取られてしまった")
	}
}

func TestOpen_空いているチューナーを先に使う(t *testing.T) {
	r := newRig(t, spec("A", "GR"), spec("B", "GR"))
	s1 := open(t, r.m, "GR", "T27", 5, "epg GR/T27")
	readOne(t, s1)
	s2 := open(t, r.m, "GR", "T21", 3, "logo GR/T21")
	readOne(t, s2)
	if channelOf(r, "A") != "T27" || channelOf(r, "B") != "T21" {
		t.Errorf("A=%q B=%q", channelOf(r, "A"), channelOf(r, "B"))
	}
	if err := readErrNonBlocking(s1); err != nil {
		t.Errorf("取り上げていない: %v", err)
	}
}

func readErrNonBlocking(s *Sink) error {
	// 溜まっているぶんを読み捨てて、壊されていないことだけ見る
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	s.q.mu.Lock()
	defer s.q.mu.Unlock()
	if s.q.closed {
		return s.q.err
	}
	return nil
}

func TestRank_受けられる種別が少ない組ほど前(t *testing.T) {
	r := newRig(t, spec("ALL", "GR", "BS", "CS"), spec("GRONLY", "GR"), spec("OFF", "GR"))
	snap := r.m.Snapshot()
	rank := func(i int) int {
		if snap[i].Rank == nil {
			return -1
		}
		return *snap[i].Rank
	}
	if rank(1) != 0 || rank(2) != 0 || rank(0) != 1 {
		t.Errorf("rank: ALL=%d GRONLY=%d OFF=%d", rank(0), rank(1), rank(2))
	}
	s := open(t, r.m, "GR", "T27", 5, "epg GR/T27")
	readOne(t, s)
	if channelOf(r, "GRONLY") != "T27" {
		t.Errorf("GR だけの組を先に使う")
	}
}

func TestRank_無効なチューナーはrankがnullで選ばれない(t *testing.T) {
	off := spec("OFF", "GR")
	off.Disabled = true
	r := newRig(t, off, spec("ON", "GR"))
	snap := r.m.Snapshot()
	if snap[0].Rank != nil || snap[1].Rank == nil {
		t.Fatalf("rank: %+v %+v", snap[0].Rank, snap[1].Rank)
	}
	s := open(t, r.m, "GR", "T27", 5, "rec 1")
	readOne(t, s)
	if channelOf(r, "ON") != "T27" {
		t.Errorf("有効なほうを使う")
	}
	if err := openErr(r.m, "BS", "BS15", 5, "rec 2"); backend.StatusOf(err) != 409 {
		t.Errorf("受けられる種別が無ければ 409: %v", err)
	}
}

func TestOpen_引き継ぎは正常に閉じる(t *testing.T) {
	r := newRig(t, spec("A", "GR"))
	old := open(t, r.m, "GR", "T27", 9, "live GR/T27")
	readOne(t, old)
	next := open(t, r.m, "GR", "T21", 10, "rec 1", "live GR/T27")
	readOne(t, next)
	err := readErr(t, old)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("正常に閉じるはず: %v", err)
	}
}

func TestOpen_名指し(t *testing.T) {
	r := newRig(t, spec("A", "GR"), spec("B", "GR"))
	s, err := r.m.Open(Request{Type: "GR", Channel: "T27", Priority: 5, Use: "rec 1", Pin: "cmd:x:B"})
	if err != nil {
		t.Fatal(err)
	}
	readOne(t, s)
	if channelOf(r, "B") != "T27" || channelOf(r, "A") != "" {
		t.Errorf("B を使う")
	}
	if _, err := r.m.Open(Request{Type: "GR", Channel: "T27", Use: "rec 2", Pin: "nothing"}); backend.StatusOf(err) != 400 {
		t.Errorf("無い名指しは 400: %v", err)
	}
	if _, err := r.m.Open(Request{Type: "BS", Channel: "BS15", Use: "rec 2", Pin: "cmd:x:B"}); backend.StatusOf(err) != 400 {
		t.Errorf("受けられない種別は 400: %v", err)
	}
	// 同じチャンネルなら優先度にかかわらず共有
	if _, err := r.m.Open(Request{Type: "GR", Channel: "T27", Priority: 1, Use: "logo x", Pin: "cmd:x:B"}); err != nil {
		t.Errorf("共有: %v", err)
	}
	// 別のチャンネルを同じか高い優先度が読んでいれば 409
	if _, err := r.m.Open(Request{Type: "GR", Channel: "T21", Priority: 5, Use: "rec 3", Pin: "cmd:x:B"}); backend.StatusOf(err) != 409 {
		t.Errorf("409 のはず: %v", err)
	}
}

func TestOpen_同期しなかったら同じ組の別のチューナーで選局し直す(t *testing.T) {
	r := newRig(t, spec("A", "GR"), spec("B", "GR"))
	r.tuners["A"].noLock.Store(true)
	s := open(t, r.m, "GR", "T27", 5, "rec 1")
	readOne(t, s)
	if channelOf(r, "B") != "T27" {
		t.Errorf("B で選局し直す")
	}
	// A は 1 時間後ろへ回るので、次は B が空いていれば B を先に試す
	s.Release()
	time.Sleep(400 * time.Millisecond)
	r.tuners["A"].noLock.Store(false)
	s2 := open(t, r.m, "GR", "T27", 5, "rec 2")
	readOne(t, s2)
	if channelOf(r, "B") != "T27" || r.tuners["A"].tunes.Load() != 1 {
		t.Errorf("同期しなかった A は後ろへ回る (A の Tune=%d)", r.tuners["A"].tunes.Load())
	}
}

func TestOpen_どれも同期しなければ名前を中点でつないで500(t *testing.T) {
	r := newRig(t, spec("A", "GR"), spec("B", "GR"), spec("C", "GR"))
	for _, ft := range r.tuners {
		ft.noLock.Store(true)
	}
	err := openErr(r.m, "GR", "T27", 5, "rec 1")
	want := "A・B: 同期しませんでした (電波が来ていないか、その周波数に放送がありません)"
	if err == nil || err.Error() != want || backend.StatusOf(err) != 500 {
		t.Fatalf("got %v", err)
	}
	if r.tuners["C"].tunes.Load() != 0 {
		t.Errorf("試すのは 2 台まで")
	}
}

func TestOpen_scanは選局し直さない(t *testing.T) {
	r := newRig(t, spec("A", "GR"), spec("B", "GR"))
	r.tuners["A"].noLock.Store(true)
	err := openErr(r.m, "GR", "T27", 5, "scan T27")
	if !errors.Is(err, backend.ErrNoLock) || r.tuners["B"].tunes.Load() != 0 {
		t.Errorf("scan は 1 台だけ: %v", err)
	}
}

func TestOpen_入力の検査(t *testing.T) {
	r := newRig(t, spec("A", "GR"))
	cases := []struct{ typ, ch, want string }{
		{"", "", "type と channel が必要です"},
		{"GR", "", "type と channel が必要です"},
		{"GR", "X1", "X1: チャンネル名を読めません"},
		{"BS", "T27", "T27: type と channel が合いません"},
	}
	for _, c := range cases {
		err := openErr(r.m, c.typ, c.ch, 0, "rec 1")
		if backend.StatusOf(err) != 400 || err.Error() != c.want {
			t.Errorf("%q %q: %v", c.typ, c.ch, err)
		}
	}
}

func TestOpen_singleServiceは同じ局どうしだけ共有_鍵の要らない利用者は相乗り(t *testing.T) {
	r := &rig{tuners: map[string]*fakeTuner{}}
	r.m = New(Config{
		Open: func(sp backend.TunerSpec) (backend.Tuner, error) {
			ft := &fakeTuner{name: sp.Name, types: sp.Types, single: true}
			r.tuners[sp.Name] = ft
			return ft, nil
		},
		Grace: 100 * time.Millisecond,
	})
	r.m.SetSpecs([]backend.TunerSpec{spec("N", "GR")}, false)
	defer r.m.Shutdown()

	a, err := r.m.Open(Request{Type: "GR", Channel: "T27", Priority: 10, Use: "rec 1", Service: 1024})
	if err != nil {
		t.Fatal(err)
	}
	readOne(t, a)
	if got := r.tuners["N"].last; got.Service != 1024 || !got.NeedsKey {
		t.Errorf("Tune に局が渡っていない: %+v", got)
	}
	// 同じ局は共有
	if _, err := r.m.Open(Request{Type: "GR", Channel: "T27", Priority: 9, Use: "live x", Service: 1024}); err != nil {
		t.Errorf("同じ局: %v", err)
	}
	// 鍵の要らない利用者は相乗りできる
	if _, err := r.m.Open(Request{Type: "GR", Channel: "T27", Priority: 5, Use: "epg GR/T27"}); err != nil {
		t.Errorf("epg: %v", err)
	}
	// 同じチャンネルの別の局は別の選局で、チューナーが 1 つなら 409
	if _, err := r.m.Open(Request{Type: "GR", Channel: "T27", Priority: 9, Use: "live y", Service: 2048}); backend.StatusOf(err) != 409 {
		t.Errorf("別の局は 409: %v", err)
	}
	if n := r.tuners["N"].tunes.Load(); n != 1 {
		t.Errorf("Tune は 1 回: %d", n)
	}
}

func TestOpen_30秒待っても届かなければ失敗(t *testing.T) {
	m := New(Config{
		Open: func(sp backend.TunerSpec) (backend.Tuner, error) {
			return &silentTuner{fakeTuner{name: sp.Name, types: sp.Types}}, nil
		},
		FirstTS: 200 * time.Millisecond,
	})
	m.SetSpecs([]backend.TunerSpec{spec("S", "GR")}, false)
	defer m.Shutdown()
	err := openErr(m, "GR", "T27", 5, "rec 1")
	if !errors.Is(err, backend.ErrNoTS) {
		t.Fatalf("got %v", err)
	}
}

type silentTuner struct{ fakeTuner }

func (s *silentTuner) Start() (io.ReadCloser, error) {
	s.fakeTuner.mu.Lock()
	defer s.fakeTuner.mu.Unlock()
	return &blockReader{stop: s.fakeTuner.stopCh}, nil
}

type blockReader struct{ stop <-chan struct{} }

func (b *blockReader) Read([]byte) (int, error) { <-b.stop; return 0, io.EOF }
func (b *blockReader) Close() error             { return nil }

func TestSetSpecs_名前が同じなら選局を保つ_消したら畳む(t *testing.T) {
	r := newRig(t, spec("A", "GR"), spec("B", "GR"))
	s := open(t, r.m, "GR", "T27", 5, "rec 1")
	readOne(t, s)
	r.m.SetSpecs([]backend.TunerSpec{spec("A", "GR"), spec("B", "GR")}, false)
	if channelOf(r, "A") != "T27" {
		t.Errorf("選局を保つ")
	}
	r.m.SetSpecs([]backend.TunerSpec{spec("B", "GR")}, false)
	if err := readErr(t, s); !errors.Is(err, ErrKilled) {
		t.Errorf("消したら畳む: %v", err)
	}
}

// 数 KB の読みが 150KB の配列を抱え込むと、列の上限 (バイト数) の何十倍ものメモリを使い、確保とクリアで CPU も尽きる
// (実機の nasne で、15 秒に 2.9MB しか流れなかった)。列に入る塊は、読めたぶんの大きさの配列にする。
func TestPump_小さい読みの塊は大きな配列を抱え込まない(t *testing.T) {
	r := newRig(t, spec("A", "GR"))
	s := open(t, r.m, "GR", "T27", 5, "rec 1")
	b := readOne(t, s) // fakeReader は 188 バイトずつ返す
	if len(b) == 0 || cap(b) > 4*len(b) {
		t.Errorf("len=%d cap=%d。読めたぶんより大きな配列を抱えている", len(b), cap(b))
	}
}

func TestSnapshot_利用者は繋いだ順に並ぶ(t *testing.T) {
	// 利用名の辞書順 (epg < rec) とは逆に繋ぐ。並びは繋いだ順 (適合テスト)
	r := newRig(t, spec("A", "GR"))
	s1 := open(t, r.m, "GR", "T27", 10, "rec 1")
	s2 := open(t, r.m, "GR", "T27", 3, "epg T27")
	readOne(t, s1)
	readOne(t, s2)
	for i := 0; i < 20; i++ { // map の走査順に頼っていないこと
		snap := r.m.Snapshot()
		if len(snap[0].Users) != 2 || snap[0].Users[0].Use != "rec 1" || snap[0].Users[1].Use != "epg T27" {
			t.Fatalf("users = %+v, want rec 1 → epg T27", snap[0].Users)
		}
	}
}

func TestNoLockErr_選局コマンドの理由を添える(t *testing.T) {
	err := noLockErr([]string{"gr0", "gr1"}, "Cannot tune to T99: no signal")
	if !errors.Is(err, backend.ErrNoLock) {
		t.Fatalf("ErrNoLock のはず: %v", err)
	}
	want := "gr0・gr1: 同期しませんでした (電波が来ていないか、その周波数に放送がありません): Cannot tune to T99: no signal"
	if err.Error() != want {
		t.Errorf("文言 %q", err.Error())
	}
	if got := noLockReason(err); got != "Cannot tune to T99: no signal" {
		t.Errorf("reason = %q", got)
	}
	// 理由が無ければ、今までの文言のまま
	if got := noLockErr([]string{"gr0"}, "").Error(); got != "gr0: 同期しませんでした (電波が来ていないか、その周波数に放送がありません)" {
		t.Errorf("文言 %q", got)
	}
}

