package app

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/b25"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/cardshare"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/config"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/logx"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/tlsx"
)

// FAKE_TUNE には、このテストの実行ファイル自身を渡す。
const helperEnv = "APP_TEST_FAKE_TUNE"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		// <FAKE_TUNE> <種別> <チャンネル名>。T62 は同期しない
		if len(os.Args) >= 3 && os.Args[2] == "T62" {
			os.Exit(1)
		}
		pkt := bytes.Repeat([]byte{0x47}, 188)
		for {
			if _, err := os.Stdout.Write(pkt); err != nil {
				os.Exit(0)
			}
			time.Sleep(3 * time.Millisecond)
		}
	}
	os.Exit(m.Run())
}

type harness struct {
	url    string
	client *http.Client
	app    *App
	fp     string // クライアントの指紋
}

func start(t *testing.T) *harness {
	return startEnv(t, nil)
}

func startEnv(t *testing.T, extra map[string]string) *harness {
	t.Helper()
	dir := t.TempDir()
	exe, _ := os.Executable()
	t.Setenv(helperEnv, "1")
	env := map[string]string{
		"TUNERS_FILE": filepath.Join(dir, "config", "tuners.json"), "CHANNELS_FILE": filepath.Join(dir, "config", "channels.json"),
		"DEVICES_FILE": filepath.Join(dir, "config", "devices.json"), "DENPA_HOME": dir, "RAW_DIR": filepath.Join(dir, "raw"),
		"FAKE_TUNE": exe, "AGENT_PAIR_TOKEN": strings.Repeat("t", 43), "AGENT_NAME": "テスト機", "DENPA_VERSION": "9.9",
	}
	for k, v := range extra {
		env[k] = v
	}
	cfg, err := config.Load(func(k string) string { return env[k] }, "linux", exe)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(cfg, logx.New(nil))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := a.Listen0()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Serve(ctx, ln); close(done) }()
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); <-done })

	// クライアント証明書
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "denpa"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	return &harness{
		url: "https://" + ln.Addr().String(),
		client: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
			Certificates: []tls.Certificate{cert}, InsecureSkipVerify: true, MaxVersion: tls.VersionTLS13,
		}}},
		app: a, fp: tlsx.Fingerprint(der),
	}
}

func (h *harness) do(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.url+path, rd)
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (h *harness) pair(t *testing.T) {
	t.Helper()
	token := strings.Repeat("t", 43)
	m := hmac.New(sha256.New, []byte(token))
	m.Write([]byte("denpa-pair/client\n" + h.app.ID.Print + "\n" + h.fp))
	proof := base64.RawURLEncoding.EncodeToString(m.Sum(nil))
	code, b := h.do(t, "POST", "/denpa/pair", map[string]string{"client": h.fp, "proof": proof})
	if code != 200 {
		t.Fatalf("組めない: %d %s", code, b)
	}
	m2 := hmac.New(sha256.New, []byte(token))
	m2.Write([]byte("denpa-pair/agent\n" + h.app.ID.Print + "\n" + h.fp))
	var res struct{ Proof string }
	json.Unmarshal(b, &res)
	if res.Proof != base64.RawURLEncoding.EncodeToString(m2.Sum(nil)) {
		t.Errorf("エージェントの proof が違う")
	}
}

func TestPair_組む前は403_組んだら通る(t *testing.T) {
	h := start(t)
	code, b := h.do(t, "GET", "/denpa/about", nil)
	if code != 403 || !strings.Contains(string(b), `"paired":false`) || !strings.Contains(string(b), "まだ組んでいません") {
		t.Fatalf("組む前: %d %s", code, b)
	}
	if code, _ := h.do(t, "POST", "/denpa/pair", map[string]string{"client": h.fp, "proof": "x"}); code != 403 {
		t.Errorf("合言葉が違えば 403: %d", code)
	}
	if _, b := h.do(t, "POST", "/denpa/pair", map[string]string{"client": "other", "proof": "x"}); !strings.Contains(string(b), "証明書が名乗りと違います") {
		t.Errorf("名乗りが違う: %s", b)
	}
	if code, b := h.do(t, "POST", "/denpa/pair", strings.Repeat("a", 2000)); code != 413 || !strings.Contains(string(b), "上限 1024 バイト") {
		t.Errorf("大きすぎる: %d %s", code, b)
	}
	h.pair(t)
	code, b = h.do(t, "GET", "/denpa/about", nil)
	var about map[string]any
	json.Unmarshal(b, &about)
	if code != 200 || about["name"] != "テスト機" || about["version"] != "9.9" || about["ccidDriver"] != nil {
		t.Errorf("about: %d %s", code, b)
	}
	if !strings.Contains(string(b), "テスト機") {
		t.Errorf("日本語をエスケープしない: %s", b)
	}
	if code, b := h.do(t, "GET", "/nothing", nil); code != 404 || string(b) != "{\"ok\":false,\"error\":\"not found\"}\n" {
		t.Errorf("404: %d %q", code, b)
	}
	if code, _ := h.do(t, "DELETE", "/denpa/pair", nil); code != 204 {
		t.Errorf("DELETE pair: %d", code)
	}
	if code, _ := h.do(t, "GET", "/denpa/about", nil); code != 403 {
		t.Errorf("外したら 403: %d", code)
	}
}

func TestStream_FAKE_TUNEで流れる_共有_取り上げ(t *testing.T) {
	h := start(t)
	h.pair(t)
	code, b := h.do(t, "PUT", "/denpa/tuners", map[string]any{"tuners": []map[string]any{
		{"name": "fake0", "types": []string{"GR"}},
	}})
	if code != 200 {
		t.Fatalf("PUT tuners: %d %s", code, b)
	}
	var tv struct {
		Tuners []struct {
			Name       string
			Rank       *int
			Receivable []string
		}
		Detected bool
	}
	json.Unmarshal(b, &tv)
	if len(tv.Tuners) != 1 || tv.Detected || tv.Tuners[0].Rank == nil || len(tv.Tuners[0].Receivable) != 5 {
		t.Fatalf("tuners: %s", b)
	}

	get := func(q string) (*http.Response, error) {
		req, _ := http.NewRequest("GET", h.url+"/denpa/stream?"+q, nil)
		return h.client.Do(req)
	}
	low, err := get("type=GR&channel=T27&priority=3&use=epg%20GR/T27")
	if err != nil {
		t.Fatal(err)
	}
	defer low.Body.Close()
	if low.StatusCode != 200 || low.Header.Get("Content-Type") != "video/MP2T" {
		t.Fatalf("stream: %d %v", low.StatusCode, low.Header)
	}
	buf := make([]byte, 188*3)
	if _, err := io.ReadFull(low.Body, buf); err != nil || buf[0] != 0x47 || buf[188] != 0x47 {
		t.Fatalf("TS ではない: %v", err)
	}

	// 取り上げ: 優先度の高い要求が来ると、送り始めていた接続は壊れる
	high, err := get("type=GR&channel=T21&priority=10&use=rec%201")
	if err != nil {
		t.Fatal(err)
	}
	defer high.Body.Close()
	if high.StatusCode != 200 {
		t.Fatalf("high: %d", high.StatusCode)
	}
	io.ReadFull(high.Body, buf)
	deadline := time.Now().Add(3 * time.Second)
	var readErr error
	for time.Now().Before(deadline) {
		if _, readErr = io.ReadFull(low.Body, buf); readErr != nil {
			break
		}
	}
	if readErr == nil || readErr == io.EOF {
		t.Errorf("正常終了ではなく壊れるはず: %v", readErr)
	}

	// 同じ優先度は 409、同期しないチャンネルは 500
	if code, b := h.do(t, "GET", "/denpa/stream?type=GR&channel=T22&priority=10&use=rec%202", nil); code != 409 ||
		!strings.Contains(string(b), "GR のチューナーに空きがありません") {
		t.Errorf("409: %d %s", code, b)
	}
	high.Body.Close()
	time.Sleep(6 * time.Second) // 余韻
	code, b = h.do(t, "GET", "/denpa/stream?type=GR&channel=T62&priority=10&use=rec%203", nil)
	if code != 500 || !strings.Contains(string(b), "fake0: 同期しませんでした (電波が来ていないか、その周波数に放送がありません)") {
		t.Errorf("同期しない: %d %s", code, b)
	}
	if code, b := h.do(t, "GET", "/denpa/stream?type=GR&channel=T27&service=0&use=rec", nil); code != 400 ||
		!strings.Contains(string(b), "service は 1〜65535 の番組番号です") {
		t.Errorf("service: %d %s", code, b)
	}
}

func TestTuners_PUTの検査(t *testing.T) {
	h := start(t)
	h.pair(t)
	cases := []struct {
		body any
		want string
	}{
		{map[string]any{"tuners": []map[string]any{{"types": []string{"GR"}}}}, "name の無いチューナーがあります"},
		{map[string]any{"tuners": []map[string]any{{"name": "x", "types": "GR"}}}, "x: types は文字列の配列にします"},
		{map[string]any{"tuners": []map[string]any{{"name": "x", "types": []any{1}}}}, "x: types は文字列の配列にします"},
		{map[string]any{"tuners": []map[string]any{{"name": "x", "device": "cmd:nothing:/dev/x"}}}, "x: cmd: の機種が機材の定義にありません"},
	}
	for _, c := range cases {
		code, b := h.do(t, "PUT", "/denpa/tuners", c.body)
		if code != 400 || !strings.Contains(string(b), c.want) {
			t.Errorf("%v: %d %s", c.want, code, b)
		}
	}
	// 空の配列は定義を消す
	h.do(t, "PUT", "/denpa/tuners", map[string]any{"tuners": []map[string]any{{"name": "a", "types": []string{"GR"}}}})
	if _, err := os.Stat(h.app.Cfg.TunersFile); err != nil {
		t.Fatalf("tuners.json が無い: %v", err)
	}
	code, b := h.do(t, "PUT", "/denpa/tuners", map[string]any{"tuners": []any{}})
	if code != 200 || !strings.Contains(string(b), `"detected":true`) {
		t.Errorf("空: %d %s", code, b)
	}
	if _, err := os.Stat(h.app.Cfg.TunersFile); err == nil {
		t.Errorf("tuners.json が残っている")
	}
}

func TestTuners_PUT_知らない形のdeviceは受けて保存し_開けない理由をerrorに出す(t *testing.T) {
	h := start(t)
	h.pair(t)
	// 付属のエージェントの /dev/dvb/… や、コマンドのような文字列。保存を断らず、実行もしない
	for _, dev := range []string{"/dev/dvb/adapter9/frontend0", "/bin/sh -c rm"} {
		code, b := h.do(t, "PUT", "/denpa/tuners", map[string]any{"tuners": []map[string]any{{"name": "x", "types": []string{"GR"}, "device": dev}}})
		if code != 200 {
			t.Fatalf("%s: %d %s", dev, code, b)
		}
		var res struct {
			Tuners []struct {
				Name   string  `json:"name"`
				Device string  `json:"device"`
				Error  *string `json:"error"`
			} `json:"tuners"`
		}
		if err := json.Unmarshal(b, &res); err != nil || len(res.Tuners) != 1 {
			t.Fatalf("%s: %v %s", dev, err, b)
		}
		if res.Tuners[0].Device != dev {
			t.Errorf("device = %q, want %q", res.Tuners[0].Device, dev)
		}
		// 開けない理由は、その場では出ないことがある (最初に使うときに出る)。出るなら、この文言
		if e := res.Tuners[0].Error; e != nil && !strings.Contains(*e, "開けません") {
			t.Errorf("error = %q", *e)
		}
	}
	// px4:・px4host:・cmd: は、今までどおり中身まで検べて 400
	if code, _ := h.do(t, "PUT", "/denpa/tuners", map[string]any{"tuners": []map[string]any{{"name": "x", "device": "cmd:nothing:/dev/x"}}}); code != 400 {
		t.Errorf("cmd: の不明な機種 = %d, want 400", code)
	}
}

func TestChannels_入れ替えと断り方(t *testing.T) {
	h := start(t)
	h.pair(t)
	put := func(body any) (int, string) {
		code, b := h.do(t, "PUT", "/denpa/channels", body)
		return code, string(b)
	}
	if c, b := put(map[string]any{}); c != 400 || !strings.Contains(b, "channels と scanned が必要です") {
		t.Errorf("%d %s", c, b)
	}
	if c, b := put(map[string]any{"channels": []any{}, "scanned": []string{"GR"}}); c != 400 || !strings.Contains(b, "チャンネルが 1 件もありません") {
		t.Errorf("%d %s", c, b)
	}
	if c, b := put(map[string]any{"channels": []any{map[string]any{"type": 1, "channel": "T27"}}, "scanned": []string{"GR"}}); c != 400 ||
		!strings.Contains(b, "scanned と、channels の type・channel は文字列で書きます") {
		t.Errorf("%d %s", c, b)
	}
	if c, b := put(map[string]any{"channels": []map[string]any{
		{"type": "GR", "channel": "T27", "services": []int{1024}}, {"type": "GR", "channel": "T21"},
	}, "scanned": []string{"GR"}}); c != 200 {
		t.Fatalf("%d %s", c, b)
	}
	if c, b := put(map[string]any{"channels": []map[string]any{{"type": "BS", "channel": "BS15"}}, "scanned": []string{"BS"}}); c != 200 ||
		!strings.Contains(b, "T21") || !strings.Contains(b, "BS15") {
		t.Errorf("ほかの種別は残す: %d %s", c, b)
	}
	_, b := h.do(t, "GET", "/denpa/channels", nil)
	var list []map[string]any
	json.Unmarshal(b, &list)
	if len(list) != 3 || list[0]["channel"] != "T21" || list[1]["channel"] != "T27" || list[2]["channel"] != "BS15" {
		t.Errorf("並び (種別の順、チャンネル名の順): %s", b)
	}
	if c, _ := h.do(t, "DELETE", "/denpa/channels", nil); c != 400 {
		t.Errorf("type が無ければ 400: %d", c)
	}
	_, b = h.do(t, "DELETE", "/denpa/channels?type=GR", nil)
	if strings.Contains(string(b), "T27") {
		t.Errorf("GR が残った: %s", b)
	}
}

func TestEvents_通知が届く(t *testing.T) {
	h := start(t)
	h.pair(t)
	req, _ := http.NewRequest("GET", h.url+"/denpa/events", nil)
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("%v", resp.Header)
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		h.do(t, "PUT", "/denpa/channels", map[string]any{"channels": []map[string]any{{"type": "GR", "channel": "T27"}}, "scanned": []string{"GR"}})
	}()
	buf := make([]byte, 0, 512)
	tmp := make([]byte, 128)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(string(buf), "event: channels") {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	if !strings.Contains(string(buf), "event: channels\ndata: {}\n\n") {
		t.Errorf("channels の通知が来ない: %q", buf)
	}
}

func TestLogs(t *testing.T) {
	h := start(t)
	h.pair(t)
	_, b := h.do(t, "GET", "/denpa/logs", nil)
	var res struct {
		Lines []struct {
			At   int64
			Text string
		}
	}
	json.Unmarshal(b, &res)
	if len(res.Lines) == 0 {
		t.Errorf("記録が空: %s", b)
	}
	for _, l := range res.Lines {
		if strings.HasPrefix(l.Text, "[agent]") {
			t.Errorf("/denpa/logs に [agent] は入れない: %q", l.Text)
		}
	}
}

// 画面の「自動検出」(定義を空にする) で、チューナーが消えたまま戻らなかった (実機の qnap872 で起きた)。
// 前回の検出と同じ結果だと入れ直さなかったため。
func TestTuners_自動検出に戻したら検出済みのチューナーが戻る(t *testing.T) {
	saved := detectors
	detectors = []Detector{func(*Env) ([]backend.TunerSpec, error) {
		return []backend.TunerSpec{{Name: "auto0", Types: []string{"GR"}}}, nil
	}}
	t.Cleanup(func() { detectors = saved })

	h := start(t)
	h.pair(t)
	if _, b := h.do(t, "GET", "/denpa/tuners", nil); !strings.Contains(string(b), "auto0") || !strings.Contains(string(b), `"detected":true`) {
		t.Fatalf("起動したら検出する: %s", b)
	}
	// 定義を書いて、それを消して自動検出へ戻す
	h.do(t, "PUT", "/denpa/tuners", map[string]any{"tuners": []map[string]any{{"name": "manual", "types": []string{"GR"}}}})
	code, b := h.do(t, "PUT", "/denpa/tuners", map[string]any{"tuners": []any{}})
	if code != 200 || !strings.Contains(string(b), "auto0") || !strings.Contains(string(b), `"detected":true`) {
		t.Fatalf("自動検出に戻したら、検出済みのチューナーが戻る: %d %s", code, b)
	}
	// 自動検出のまま、もう一度空にしても戻る (前回と同じ結果でも入れ直す)
	code, b = h.do(t, "PUT", "/denpa/tuners", map[string]any{"tuners": []any{}})
	if code != 200 || !strings.Contains(string(b), "auto0") {
		t.Fatalf("2 回目も戻る: %d %s", code, b)
	}
}

// fakeShareLocal は試験で鍵を配る側に渡すカードです。決まった値を返します。
type fakeShareLocal struct{}

func (fakeShareLocal) Info() (b25.Info, error) {
	info := b25.Info{
		CASystemID: 0x0005,
		IDs:        []b25.CardID{{0x00, 0x01, 0x23, 0x45, 0x67, 0x89}},
	}
	for i := range info.SystemKey {
		info.SystemKey[i] = byte(0x10 + i)
	}
	for i := range info.CBCInit {
		info.CBCInit[i] = byte(0xa0 + i)
	}
	return info, nil
}

func (fakeShareLocal) ECM(ecm []byte) (b25.ECMResult, error) {
	var answer b25.ECMResult
	for i := range answer.Odd {
		answer.Odd[i] = 0x11
	}
	for i := range answer.Even {
		answer.Even[i] = 0x22
	}
	answer.Code = 0x0200
	return answer, nil
}

func (fakeShareLocal) Confirm(ctx context.Context, verification []byte, mjd int) (int, error) {
	return 0x0800, nil
}

func (fakeShareLocal) EMMNow(payload []byte) (int, error) { return 0x2100, nil }

// TestApp_CardURL_配る側のカード番号を返す は、CARD_URL を書いたとき、
// /denpa/card が source "remote" と配る側のカード番号を返すことを確かめます。
func TestApp_CardURL_配る側のカード番号を返す(t *testing.T) {
	nets, err := config.ParseShareNetworks(config.DefaultShareNetworks())
	if err != nil {
		t.Fatal(err)
	}
	share := cardshare.New(0, nets, fakeShareLocal{}, nil, 1, 0, func(format string, args ...any) {})
	if err := share.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = share.Close() })
	_, port, err := net.SplitHostPort(share.Addr())
	if err != nil {
		t.Fatal(err)
	}
	url := "http://127.0.0.1:" + port
	h := startEnv(t, map[string]string{"CARD_URL": url})
	h.pair(t)
	code, b := h.do(t, "GET", "/denpa/card", nil)
	if code != 200 {
		t.Fatalf("/denpa/card が %d: %s", code, b)
	}
	var res map[string]any
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	if res["source"] != "remote" {
		t.Fatalf("source が remote ではありません: %s", b)
	}
	if res["remote"] != url {
		t.Fatalf("remote が違います: %s", b)
	}
	ids, ok := res["ids"].([]any)
	if !ok || len(ids) != 1 || ids[0] != "0000004886718345" {
		t.Fatalf("ids が違います: %s", b)
	}
	readers, ok := res["readers"].([]any)
	if !ok || len(readers) != 0 {
		t.Fatalf("readers が空ではありません: %s", b)
	}
	if res["ok"] != true {
		t.Fatalf("ok が true ではありません: %s", b)
	}
}
