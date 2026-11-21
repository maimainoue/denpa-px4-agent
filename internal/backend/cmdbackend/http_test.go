package cmdbackend

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/devices"
)

// newHTTPTestTuner は h を待ち受けにした url の機種で、チューナーを 1 つ作る。
// url にテストのサーバーのポートを埋め、device は 127.0.0.1 にする
// (checkDeviceHost は {device} の途中の : を許さないため)。
func newHTTPTestTuner(t *testing.T, h http.Handler, edit func(*devices.Def)) (backend.Tuner, *recorder) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	return newHTTPTestTunerAt(t, port, edit)
}

func newHTTPTestTunerAt(t *testing.T, port string, edit func(*devices.Def)) (backend.Tuner, *recorder) {
	t.Helper()
	def := devices.Def{
		Model:         "httptest",
		URL:           "http://{device}:" + port + "/tuner/{channel}{sidQuery}",
		ChannelFormat: devices.FormatRecpt1,
		SingleService: true, DecryptedByDevice: true,
		Receivers: []devices.Receiver{{Index: 0, Types: []string{"GR", "BS", "CS"}}},
	}
	if edit != nil {
		edit(&def)
	}
	if err := def.Validate(); err != nil {
		t.Fatal(err)
	}
	defs := devices.NewDefs()
	defs.Put(def)
	rec := &recorder{}
	b := New(Config{Defs: defs, Log: rec.log, StopGrace: 300 * time.Millisecond, FirstTSTimeout: 3 * time.Second})
	tn, err := b.Open(backend.TunerSpec{Name: "N0", Types: []string{"GR"}, Device: "cmd:httptest:127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tn.Close() })
	return tn, rec
}

func tsHandler(path *atomic.Value) http.HandlerFunc {
	packet := bytes.Repeat([]byte{0x47}, 188)
	return func(w http.ResponseWriter, r *http.Request) {
		if path != nil {
			path.Store(r.URL.RequestURI())
		}
		w.Header().Set("Content-Type", "video/MP2T")
		for {
			if _, err := w.Write(packet); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
}

var tuneReq = backend.Tuning{Channel: "T21", Service: 1056, NeedsKey: true}

func TestHTTP_200ならTSを読めてURLが展開される(t *testing.T) {
	var got atomic.Value
	tn, _ := newHTTPTestTuner(t, tsHandler(&got), nil)
	if err := tn.Tune(tuneReq); err != nil {
		t.Fatal(err)
	}
	r, err := tn.Start()
	if err != nil {
		t.Fatal(err)
	}
	data := readAtLeast(t, r, 188*5)
	if data[0] != 0x47 {
		t.Errorf("TS の先頭 %#x", data[0])
	}
	if got.Load() != "/tuner/21?sid=1056" {
		t.Errorf("要求 %v", got.Load())
	}
}

func TestHTTP_503は使用中の409(t *testing.T) {
	tn, _ := newHTTPTestTuner(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "busy", 503)
	}), nil)
	err := tn.Tune(tuneReq)
	if !errors.Is(err, backend.ErrBusy) || backend.StatusOf(err) != 409 {
		t.Fatalf("got %v (%d)", err, backend.StatusOf(err))
	}
	if err.Error() != "GR のチューナーに空きがありません" {
		t.Errorf("文言 %q", err.Error())
	}
}

func TestHTTP_404は同期しなかった(t *testing.T) {
	tn, _ := newHTTPTestTuner(t, http.NotFoundHandler(), nil)
	err := tn.Tune(tuneReq)
	if !errors.Is(err, backend.ErrNoLock) {
		t.Fatalf("got %v", err)
	}
	if errors.Is(err, backend.ErrBusy) {
		t.Error("使用中とは言わない")
	}
}

func TestHTTP_500は使用中でも同期しなかったでもなく理由が伝わる(t *testing.T) {
	tn, rec := newHTTPTestTuner(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "B-CAS ACTIVATE failed", 500)
	}), nil)
	err := tn.Tune(tuneReq)
	if err == nil {
		t.Fatal("成功した")
	}
	if errors.Is(err, backend.ErrBusy) || errors.Is(err, backend.ErrNoLock) {
		t.Errorf("使用中・同期しなかったに見える: %v", err)
	}
	if backend.StatusOf(err) != 500 || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("got %v (%d)", err, backend.StatusOf(err))
	}
	if !rec.has("B-CAS ACTIVATE failed") {
		t.Error("機材の返した本文が記録に出ていない")
	}
}

func TestHTTP_繋がらないときは使用中に見せない(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close() // 閉じたポートに繋ぐ
	tn, _ := newHTTPTestTunerAt(t, port, nil)
	err = tn.Tune(tuneReq)
	if err == nil || errors.Is(err, backend.ErrBusy) || errors.Is(err, backend.ErrNoLock) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP で繋がりません") {
		t.Errorf("文言 %q", err.Error())
	}
}

func TestHTTP_リダイレクトは追わない(t *testing.T) {
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		tsHandler(nil)(w, r)
	}))
	defer other.Close()
	tn, _ := newHTTPTestTuner(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/x", http.StatusTemporaryRedirect)
	}), nil)
	err := tn.Tune(tuneReq)
	if err == nil || !strings.Contains(err.Error(), "HTTP 307") {
		t.Fatalf("got %v", err)
	}
	if hits.Load() != 0 {
		t.Errorf("別のホストへ飛んだ (%d 回)", hits.Load())
	}
}

func TestHTTP_Stopで接続が切れる(t *testing.T) {
	closed := make(chan struct{})
	tn, _ := newHTTPTestTuner(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tsHandler(nil)(w, r)
		close(closed)
	}), nil)
	if err := tn.Tune(tuneReq); err != nil {
		t.Fatal(err)
	}
	r, _ := tn.Start()
	readAtLeast(t, r, 188*2)
	if err := tn.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop しても、機材への接続が切れない")
	}
}

func TestHTTP_再選局で前の接続を切って新しく繋ぐ(t *testing.T) {
	var path atomic.Value
	var active atomic.Int32
	tn, _ := newHTTPTestTuner(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		active.Add(1)
		defer active.Add(-1)
		tsHandler(&path)(w, r)
	}), nil)
	for _, ch := range []string{"T21", "T22"} {
		if err := tn.Tune(backend.Tuning{Channel: ch, Service: 1056, NeedsKey: true}); err != nil {
			t.Fatal(ch, err)
		}
	}
	if path.Load() != "/tuner/22?sid=1056" {
		t.Errorf("2 回目の要求 %v", path.Load())
	}
	deadline := time.Now().Add(2 * time.Second)
	for active.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if active.Load() != 1 {
		t.Errorf("同時に繋がっているのは %d 本 (1 本のはず)", active.Load())
	}
}

func TestHTTP_本文が途中で切れたら読み出しが理由を返す(t *testing.T) {
	tn, _ := newHTTPTestTuner(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100000") // 約束より先に閉じる
		w.Write(bytes.Repeat([]byte{0x47}, 188*4))
		w.(http.Flusher).Flush()
		hj, _ := w.(http.Hijacker)
		c, _, _ := hj.Hijack()
		c.Close()
	}), nil)
	if err := tn.Tune(tuneReq); err != nil {
		t.Fatal(err)
	}
	r, _ := tn.Start()
	_, err := io.ReadAll(r)
	if err == nil || !strings.Contains(err.Error(), "途中で切れました") {
		t.Fatalf("got %v", err)
	}
}

func TestHTTP_deviceにパスを混ぜられない(t *testing.T) {
	defs := devices.NewDefs()
	defs.Put(devices.Def{Model: "httptest", URL: "http://{device}:8301/tuner/{channel}", ChannelFormat: devices.FormatRecpt1,
		Receivers: []devices.Receiver{{Index: 0, Types: []string{"GR"}}}})
	b := New(Config{Defs: defs})
	for _, dev := range []string{"cmd:httptest:evil.example/x#", "cmd:httptest:a@b", "cmd:httptest:h:1"} {
		if _, err := b.Open(backend.TunerSpec{Name: "N", Types: []string{"GR"}, Device: dev}); err == nil {
			t.Errorf("%s を受けた", dev)
		}
	}
	if _, err := b.Open(backend.TunerSpec{Name: "N", Types: []string{"GR"}, Device: "cmd:httptest:192.168.0.43"}); err != nil {
		t.Errorf("正しい host を断った: %v", err)
	}
}

func TestHTTP_200で本文が空のまま閉じたら同期しなかった(t *testing.T) {
	tn, _ := newHTTPTestTuner(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200) // 本文なし
	}), nil)
	err := tn.Tune(tuneReq)
	if !errors.Is(err, backend.ErrNoLock) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "HTTP 200 を返しました") {
		t.Errorf("200 を失敗として返している: %v", err)
	}
}

func TestHTTP_200のあと最初のTSの前に切れたら理由が伝わる(t *testing.T) {
	tn, _ := newHTTPTestTuner(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100000") // 約束したのに、1 バイトも送らず閉じる
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		hj, _ := w.(http.Hijacker)
		c, _, _ := hj.Hijack()
		c.Close()
	}), nil)
	err := tn.Tune(tuneReq)
	if err == nil || errors.Is(err, backend.ErrBusy) || errors.Is(err, backend.ErrNoLock) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP で繋がりません") {
		t.Errorf("理由が隠れている: %v", err)
	}
}

func TestHTTP_503の本文が終わらなくても使用中の409(t *testing.T) {
	done := make(chan struct{})
	tn, _ := newHTTPTestTuner(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		w.Write([]byte("busy"))
		w.(http.Flusher).Flush()
		<-done // 本文を閉じない
	}), nil)
	// srv.Close はハンドラの終わりを待つので、その前 (Cleanup は後に足したものから先に走る) に解放する
	t.Cleanup(func() { close(done) })
	began := time.Now()
	err := tn.Tune(tuneReq)
	if !errors.Is(err, backend.ErrBusy) || backend.StatusOf(err) != 409 {
		t.Fatalf("got %v (%d)", err, backend.StatusOf(err))
	}
	if d := time.Since(began); d > 2800*time.Millisecond {
		t.Errorf("本文を待ちすぎ: %v", d)
	}
}

func TestHTTP_503と404でも本文の先頭を記録に出す(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{{503, "locked by other"}, {404, "no signal"}} {
		tn, rec := newHTTPTestTuner(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, tc.body, tc.status)
		}), nil)
		if err := tn.Tune(tuneReq); err == nil {
			t.Fatal("成功した")
		}
		if !rec.has(tc.body) {
			t.Errorf("%d の本文 %q が記録に出ていない", tc.status, tc.body)
		}
	}
}
