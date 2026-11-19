package cmdbackend

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/devices"
)

// テストの実行ファイル自身を子プロセスにする。
//
//	helper <動作> [引数…]
//	  ts            188 バイトの TS (0x47 で始まる) を書き続ける。引数はそのまま標準出力に 1 行目として書く
//	  exit <コード> 何も書かずに終わる
//	  late-exit <コード> 少し書いてから終わる
//	  silent        何も書かず、止められるまで待つ
//	  ignore-term   SIGTERM を無視して TS を書き続ける
//	  stderr        標準エラーに行を出してから TS を書き続ける
const helperEnv = "CMDBACKEND_TEST_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		helperMain(os.Args[1:])
		return
	}
	os.Exit(m.Run())
}

func helperMain(args []string) {
	if len(args) == 0 {
		os.Exit(2)
	}
	packet := bytes.Repeat([]byte{0x47}, 188)
	stream := func() {
		for {
			if _, err := os.Stdout.Write(packet); err != nil {
				os.Exit(0)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	switch args[0] {
	case "ts":
		fmt.Fprintf(os.Stdout, "ARGS:%s\n", strings.Join(args[1:], " "))
		stream()
	case "exit":
		code, _ := strconv.Atoi(args[1])
		os.Exit(code)
	case "late-exit":
		code, _ := strconv.Atoi(args[1])
		os.Stdout.Write(packet)
		time.Sleep(50 * time.Millisecond)
		os.Exit(code)
	case "silent":
		time.Sleep(time.Minute)
	case "ignore-term":
		ignoreTerm()
		stream()
	case "stderr":
		fmt.Fprint(os.Stderr, "C/N = 12.3dB\rC/N = 12.5dB\rhello\n")
		stream()
	}
	os.Exit(0)
}

type recorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *recorder) log(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

func (r *recorder) has(sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// newTestTuner は helper の動作 mode を command にした機種で、チューナーを 1 つ作る。
func newTestTuner(t *testing.T, mode []string, edit func(*devices.Def)) (backend.Tuner, *recorder) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	def := devices.Def{
		Model:         "test",
		Command:       append([]string{exe}, append(mode, "{type}", "{channel}")...),
		ChannelFormat: devices.FormatDenpa,
		Env:           map[string]string{helperEnv: "1"},
		Receivers:     []devices.Receiver{{Index: 0, Types: []string{"GR", "BS", "CS"}}},
	}
	if edit != nil {
		edit(&def)
	}
	defs := devices.NewDefs()
	defs.Put(def)
	rec := &recorder{}
	b := New(Config{
		Defs:           defs,
		Log:            rec.log,
		StopGrace:      300 * time.Millisecond,
		FirstTSTimeout: 3 * time.Second,
	})
	tn, err := b.Open(backend.TunerSpec{Name: "T0", Types: []string{"GR"}, Device: "cmd:test:/dev/x"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tn.Close() })
	return tn, rec
}

func readAtLeast(t *testing.T, r io.Reader, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("TS を読めません: %v", err)
	}
	return buf
}

func TestTune_TSが流れる(t *testing.T) {
	tn, _ := newTestTuner(t, []string{"ts"}, nil)
	if err := tn.Tune(backend.Tuning{Type: "GR", Channel: "T27"}); err != nil {
		t.Fatal(err)
	}
	r, err := tn.Start()
	if err != nil {
		t.Fatal(err)
	}
	got := readAtLeast(t, r, 100)
	// 最初の塊も落とさず、先頭から返す (子は先頭に ARGS 行を書く)
	if !strings.HasPrefix(string(got), "ARGS:GR T27\n") {
		t.Fatalf("先頭が違う: %q", got)
	}
	if sig, err := tn.Signal(); sig != nil || err != nil {
		t.Errorf("signal は null: %v %v", sig, err)
	}
	if err := tn.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(r); err != nil {
		t.Errorf("Stop で止めたときは EOF: %v", err)
	}
}

func TestTune_選局し直しは前を止める(t *testing.T) {
	tn, _ := newTestTuner(t, []string{"ts"}, nil)
	if err := tn.Tune(backend.Tuning{Channel: "T27"}); err != nil {
		t.Fatal(err)
	}
	first, _ := tn.Start()
	if err := tn.Tune(backend.Tuning{Channel: "T28"}); err != nil {
		t.Fatal(err)
	}
	// 前の読み出しは終わる
	if _, err := io.ReadAll(first); err != nil {
		t.Errorf("前の選局の読み出し: %v", err)
	}
	second, err := tn.Start()
	if err != nil {
		t.Fatal(err)
	}
	if got := readAtLeast(t, second, 20); !strings.HasPrefix(string(got), "ARGS:GR T28") {
		t.Errorf("新しい選局のはず: %q", got)
	}
}

func TestTune_非0で終わったら同期しなかった(t *testing.T) {
	tn, rec := newTestTuner(t, []string{"exit", "1"}, nil)
	err := tn.Tune(backend.Tuning{Channel: "T27"})
	if !errors.Is(err, backend.ErrNoLock) {
		t.Fatalf("got %v", err)
	}
	want := "T0: 同期しませんでした (電波が来ていないか、その周波数に放送がありません)"
	if err.Error() != want {
		t.Errorf("文言 %q", err.Error())
	}
	if backend.StatusOf(err) != 500 {
		t.Errorf("500 のはず: %d", backend.StatusOf(err))
	}
	if !rec.has("終了コード 1") {
		t.Errorf("記録に出ていない: %q", rec.lines)
	}
	if _, err := tn.Start(); err == nil {
		t.Errorf("失敗した選局は Start できない")
	}
}

func TestTune_0で終わってもTSが無ければ同期しなかった(t *testing.T) {
	tn, _ := newTestTuner(t, []string{"exit", "0"}, nil)
	if err := tn.Tune(backend.Tuning{Channel: "T27"}); !errors.Is(err, backend.ErrNoLock) {
		t.Fatalf("got %v", err)
	}
}

func TestTune_busyExitCodeは409(t *testing.T) {
	tn, _ := newTestTuner(t, []string{"exit", "3"}, func(d *devices.Def) {
		code := 3
		d.BusyExitCode = &code
	})
	err := tn.Tune(backend.Tuning{Channel: "T27"})
	if !errors.Is(err, backend.ErrBusy) || backend.StatusOf(err) != 409 {
		t.Fatalf("got %v (%d)", err, backend.StatusOf(err))
	}
	if err.Error() != "GR のチューナーに空きがありません" {
		t.Errorf("文言 %q", err.Error())
	}
	if errors.Is(err, backend.ErrNoLock) {
		t.Errorf("同期しなかったとは言わない")
	}
}

func TestTune_busyExitCode以外の非0は同期しなかった(t *testing.T) {
	tn, _ := newTestTuner(t, []string{"exit", "1"}, func(d *devices.Def) {
		code := 3
		d.BusyExitCode = &code
	})
	if err := tn.Tune(backend.Tuning{Channel: "T27"}); !errors.Is(err, backend.ErrNoLock) {
		t.Fatalf("got %v", err)
	}
}

func TestTune_TSを出してから終わったら読み出しが理由を返す(t *testing.T) {
	tn, _ := newTestTuner(t, []string{"late-exit", "7"}, nil)
	if err := tn.Tune(backend.Tuning{Channel: "T27"}); err != nil {
		t.Fatal(err)
	}
	r, _ := tn.Start()
	_, err := io.ReadAll(r)
	if err == nil || !strings.Contains(err.Error(), "終了コード 7") {
		t.Fatalf("got %v", err)
	}
}

func TestTune_最初のTSが来なければ失敗で畳む(t *testing.T) {
	tn, _ := newTestTuner(t, []string{"silent"}, nil)
	began := time.Now()
	b := tn.(*tuner).b
	b.cfg.FirstTSTimeout = 300 * time.Millisecond
	err := tn.Tune(backend.Tuning{Channel: "T27"})
	if !errors.Is(err, backend.ErrNoTS) {
		t.Fatalf("got %v", err)
	}
	if err.Error() != "T0: 30 秒待っても TS が届きませんでした" {
		t.Errorf("文言 %q", err.Error())
	}
	if time.Since(began) > 5*time.Second {
		t.Errorf("子を止められず待たされた: %v", time.Since(began))
	}
}

func TestTune_止まらない子はSIGKILLする(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows にはシグナルが無い")
	}
	tn, _ := newTestTuner(t, []string{"ignore-term"}, nil)
	if err := tn.Tune(backend.Tuning{Channel: "T27"}); err != nil {
		t.Fatal(err)
	}
	s := tn.(*tuner).cur
	began := time.Now()
	if err := tn.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.done:
	default:
		t.Fatal("Stop が返ったのに子が残っている")
	}
	if d := time.Since(began); d < 250*time.Millisecond || d > 3*time.Second {
		t.Errorf("SIGTERM のあと grace (300ms) 待ってから SIGKILL のはず: %v", d)
	}
}

func TestTune_標準エラーは名前つきで記録に流す(t *testing.T) {
	tn, rec := newTestTuner(t, []string{"stderr"}, nil)
	if err := tn.Tune(backend.Tuning{Channel: "T27"}); err != nil {
		t.Fatal(err)
	}
	tn.Stop()
	for _, want := range []string{"[T0] C/N = 12.3dB", "[T0] C/N = 12.5dB", "[T0] hello"} {
		if !rec.has(want) {
			t.Errorf("%q が記録に無い: %q", want, rec.lines)
		}
	}
}

func TestTune_機材に触る前の400(t *testing.T) {
	cases := []struct {
		name string
		edit func(*devices.Def)
		tn   backend.Tuning
		want string
	}{
		{"読めないチャンネル", nil, backend.Tuning{Channel: "X1"}, "X1: チャンネル名を読めません"},
		{"BS07", nil, backend.Tuning{Channel: "BS07"}, "BS07: BS07 と BS17 は 4K/8K のため選局できません"},
		{"種別と合わない", nil, backend.Tuning{Type: "BS", Channel: "T27"}, "T0: T27 は BS のチャンネルではありません"},
		{"受けられない種別", nil, backend.Tuning{Channel: "QC30"}, "T0: CATV は受けられません"},
		{"TSID は渡せない (recpt1)", func(d *devices.Def) { d.ChannelFormat = devices.FormatRecpt1 },
			backend.Tuning{Channel: "BS15:16625"}, "T0: このコマンドでは TSID を指定できません"},
		{"recpt1 に ISDB-C は渡せない", func(d *devices.Def) {
			d.ChannelFormat = devices.FormatRecpt1
			d.Receivers[0].Types = []string{"CATV"}
		}, backend.Tuning{Channel: "QC30:2"}, "T0: このコマンドでは QC30:2 を選局できません"},
		{"singleService は鍵の要る選局に service が要る", func(d *devices.Def) { d.SingleService = true },
			backend.Tuning{Channel: "T27", NeedsKey: true}, "T0: この機材は観る局 (service) が要ります"},
	}
	for _, c := range cases {
		tn, _ := newTestTuner(t, []string{"ts"}, c.edit)
		err := tn.Tune(c.tn)
		if err == nil || backend.StatusOf(err) != 400 || err.Error() != c.want {
			t.Errorf("%s: got %v (%d)\n want 400 %q", c.name, err, backend.StatusOf(err), c.want)
		}
		if tn.(*tuner).cur != nil {
			t.Errorf("%s: 子を起動してしまった", c.name)
		}
	}
}

func TestTune_検査に落ちても前の選局は残る(t *testing.T) {
	tn, _ := newTestTuner(t, []string{"ts"}, nil)
	if err := tn.Tune(backend.Tuning{Channel: "T27"}); err != nil {
		t.Fatal(err)
	}
	if err := tn.Tune(backend.Tuning{Channel: "X1"}); backend.StatusOf(err) != 400 {
		t.Fatalf("got %v", err)
	}
	if _, err := tn.Start(); err != nil {
		t.Errorf("前の選局が消えた: %v", err)
	}
}

func TestTune_singleServiceは鍵の要らない利用者なら局が無くてよい(t *testing.T) {
	tn, _ := newTestTuner(t, []string{"ts"}, func(d *devices.Def) {
		d.SingleService = true
		d.ServiceArg = []string{"--sid", "{service}"}
	})
	if err := tn.Tune(backend.Tuning{Channel: "T27"}); err != nil {
		t.Fatal(err)
	}
	r, _ := tn.Start()
	if got := readAtLeast(t, r, 30); !strings.HasPrefix(string(got), "ARGS:GR T27\n") {
		t.Errorf("局を渡してはいけない: %q", got)
	}
	if err := tn.Tune(backend.Tuning{Channel: "T27", Service: 23608, NeedsKey: true}); err != nil {
		t.Fatal(err)
	}
	r, _ = tn.Start()
	if got := readAtLeast(t, r, 40); !strings.HasPrefix(string(got), "ARGS:GR T27 --sid 23608\n") {
		t.Errorf("serviceArg が足されていない: %q", got)
	}
}

func TestSetLnb(t *testing.T) {
	tn, _ := newTestTuner(t, []string{"ts"}, func(d *devices.Def) {
		d.LNB = []int{15}
		d.LnbArg = map[string][]string{"15v": {"--lnb", "15"}}
		d.Command = append(d.Command[:len(d.Command)-2:len(d.Command)-2], "{lnb}", "{type}", "{channel}")
	})
	if err := tn.SetLnb(11); !errors.Is(err, backend.ErrUnsupported) || backend.StatusOf(err) != 400 {
		t.Errorf("11v は出せない: %v", err)
	} else if err.Error() != "T0: この機材は 11v を出せません" {
		t.Errorf("文言 %q", err.Error())
	}
	if err := tn.SetLnb(5); backend.StatusOf(err) != 400 {
		t.Errorf("5V は読めない: %v", err)
	}
	if err := tn.SetLnb(15); err != nil {
		t.Fatal(err)
	}

	// 衛星では {lnb} が展開される。ARGS 行は mode の後ろ全部 (--lnb 15 BS ...) を写す
	if err := tn.Tune(backend.Tuning{Channel: "BS15"}); err != nil {
		t.Fatal(err)
	}
	r, _ := tn.Start()
	if got := readAtLeast(t, r, 30); !strings.HasPrefix(string(got), "ARGS:--lnb 15 BS BS15\n") {
		t.Errorf("衛星の給電が引数に写っていない: %q", got)
	}
	// 衛星でない選局では給電を触らない
	if err := tn.Tune(backend.Tuning{Channel: "T27"}); err != nil {
		t.Fatal(err)
	}
	r, _ = tn.Start()
	if got := readAtLeast(t, r, 30); !strings.HasPrefix(string(got), "ARGS:GR T27\n") {
		t.Errorf("地上波で給電の引数が付いた: %q", got)
	}
}

func TestSetLnb_lnbが空の機種は給電できない(t *testing.T) {
	tn, _ := newTestTuner(t, []string{"ts"}, func(d *devices.Def) { d.LNB = []int{} })
	if err := tn.SetLnb(15); !errors.Is(err, backend.ErrUnsupported) {
		t.Errorf("got %v", err)
	}
	if err := tn.SetLnb(0); err != nil {
		t.Errorf("0 は通る: %v", err)
	}
}

func TestClose_選局中なら止める(t *testing.T) {
	tn, _ := newTestTuner(t, []string{"ts"}, nil)
	if err := tn.Tune(backend.Tuning{Channel: "T27"}); err != nil {
		t.Fatal(err)
	}
	s := tn.(*tuner).cur
	if err := tn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.done:
	default:
		t.Error("子が残っている")
	}
	if err := tn.Tune(backend.Tuning{Channel: "T27"}); err == nil {
		t.Error("閉じたあとは選局できない")
	}
}

func TestStop_選局を待っている間にも効く(t *testing.T) {
	tn, _ := newTestTuner(t, []string{"silent"}, nil)
	errc := make(chan error, 1)
	go func() { errc <- tn.Tune(backend.Tuning{Channel: "T27"}) }()
	time.Sleep(300 * time.Millisecond)
	tn.Stop()
	select {
	case err := <-errc:
		if !errors.Is(err, backend.ErrAborted) {
			t.Errorf("got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop しても Tune が返らない")
	}
}

func TestStart_選局していなければ断る(t *testing.T) {
	tn, _ := newTestTuner(t, []string{"ts"}, nil)
	if _, err := tn.Start(); err == nil {
		t.Error("選局していないのに Start できた")
	}
	if err := tn.Stop(); err != nil {
		t.Errorf("選局していなくても Stop は通る: %v", err)
	}
}

func TestTune_コマンドが無ければ起動できない(t *testing.T) {
	tn, _ := newTestTuner(t, []string{"ts"}, func(d *devices.Def) {
		d.Command = []string{"/nonexistent/recpt1-for-test", "{channel}"}
	})
	err := tn.Tune(backend.Tuning{Channel: "T27"})
	if err == nil || errors.Is(err, backend.ErrNoLock) || !strings.Contains(err.Error(), "コマンドを起動できません") {
		t.Fatalf("got %v", err)
	}
}
