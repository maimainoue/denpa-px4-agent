package cardshare

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/b25"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/config"
)

// fakeLocal は試験カードを配る側に渡します。決まった値を返します。
type fakeLocal struct{}

func (fakeLocal) Info() (b25.Info, error) {
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

func (fakeLocal) ECM(ecm []byte) (b25.ECMResult, error) {
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

func (fakeLocal) Confirm(ctx context.Context, verification []byte, mjd int) (int, error) {
	return 0x0800, nil
}

func (fakeLocal) EMMNow(payload []byte) (int, error) {
	return 0x2100, nil
}

func startShareServer(t *testing.T, networks string) *Server {
	t.Helper()
	nets, err := config.ParseShareNetworks(networks)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(0, nets, fakeLocal{}, func(format string, args ...any) {})
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// shareURL は試験の配る側の URL です。待ち受けは 0.0.0.0 ですが、
// 0.0.0.0 への接続はこの環境では弾かれるので、ループバックで繋ぎます。
func shareURL(srv *Server) string {
	return "http://127.0.0.1:" + sharePort(srv)
}

func sharePort(srv *Server) string {
	_, port, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		panic(err)
	}
	return port
}

func post(t *testing.T, url string, body []byte) (int, []byte) {
	t.Helper()
	resp, err := http.Post(url, "application/octet-stream", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestShareServerRoundTrip(t *testing.T) {
	srv := startShareServer(t, config.DefaultShareNetworks())
	base := shareURL(srv)

	resp, err := http.Get(base + "/denpa/card/init")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("init が %d になりました: %s", resp.StatusCode, body)
	}
	init, err := ReadInit(body)
	if err != nil {
		t.Fatal(err)
	}
	if init.CASystemID != 0x0005 || len(init.IDs) == 0 || init.IDs[0] != (b25.CardID{0x00, 0x01, 0x23, 0x45, 0x67, 0x89}) {
		t.Fatalf("init が違います: %+v", init)
	}
	if init.SystemKey[0] != 0x10 || init.CBCInit[0] != 0xa0 {
		t.Fatalf("init の中身が違います: %+v", init)
	}

	status, body := post(t, base+"/denpa/card/ecm", []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0xaa, 0xbb, 0xcc})
	if status != http.StatusOK {
		t.Fatalf("ecm が %d になりました: %s", status, body)
	}
	answer, err := ReadECM(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range answer.Odd {
		if b != 0x11 {
			t.Fatalf("奇数鍵が違います: %x", answer.Odd)
		}
	}
	for _, b := range answer.Even {
		if b != 0x22 {
			t.Fatalf("偶数鍵が違います: %x", answer.Even)
		}
	}
	if answer.Code != 0x0200 {
		t.Fatalf("ECM の返り値が違います: 0x%04x", answer.Code)
	}

	status, body = post(t, base+"/denpa/card/confirm", []byte{0xe0, 0x00, 1, 2})
	if status != http.StatusOK {
		t.Fatalf("confirm が %d になりました: %s", status, body)
	}
	code, err := ReadCode(body, "契約確認")
	if err != nil {
		t.Fatal(err)
	}
	if code != 0x0800 {
		t.Fatalf("契約確認の返り値が違います: 0x%04x", code)
	}

	status, body = post(t, base+"/denpa/card/emm", make([]byte, 13))
	if status != http.StatusOK {
		t.Fatalf("emm が %d になりました: %s", status, body)
	}
	code, err = ReadCode(body, "EMM")
	if err != nil {
		t.Fatal(err)
	}
	if code != 0x2100 {
		t.Fatalf("EMM の返り値が違います: 0x%04x", code)
	}
}

func TestShareServerRefusesOutsideLAN(t *testing.T) {
	// 192.168.1.0/24 だけに狭めた配る側に、ループバックから繋ぎます。
	srv := startShareServer(t, "192.168.1.0/24")
	resp, err := http.Get(shareURL(srv) + "/denpa/card/init")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("403 になっていません: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "CARD_SHARE_NETWORKS") {
		t.Fatalf("断る理由が違います: %s", body)
	}
}

func TestShareServerNotFound(t *testing.T) {
	srv := startShareServer(t, config.DefaultShareNetworks())
	resp, err := http.Get(shareURL(srv) + "/denpa/card/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("404 になっていません: %d", resp.StatusCode)
	}
}

func TestShareServerMethodNotAllowed(t *testing.T) {
	srv := startShareServer(t, config.DefaultShareNetworks())
	base := shareURL(srv)
	if status, _ := post(t, base+"/denpa/card/init", nil); status != http.StatusMethodNotAllowed {
		t.Fatalf("init への POST が 405 になっていません: %d", status)
	}
	resp, err := http.Get(base + "/denpa/card/ecm")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("ecm への GET が 405 になっていません: %d", resp.StatusCode)
	}
}

func TestShareServerBadBodies(t *testing.T) {
	srv := startShareServer(t, config.DefaultShareNetworks())
	base := shareURL(srv)
	if status, _ := post(t, base+"/denpa/card/ecm", nil); status != http.StatusBadRequest {
		t.Fatalf("空の ECM が 400 になっていません: %d", status)
	}
	if status, _ := post(t, base+"/denpa/card/ecm", make([]byte, 256)); status != http.StatusBadRequest {
		t.Fatalf("256 バイトの ECM が 400 になっていません: %d", status)
	}
	if status, _ := post(t, base+"/denpa/card/confirm", []byte{0xe0, 0x00}); status != http.StatusBadRequest {
		t.Fatalf("2 バイトの契約確認が 400 になっていません: %d", status)
	}
	if status, _ := post(t, base+"/denpa/card/emm", make([]byte, 12)); status != http.StatusBadRequest {
		t.Fatalf("12 バイトの EMM が 400 になっていません: %d", status)
	}
	if status, _ := post(t, base+"/denpa/card/emm", make([]byte, 256)); status != http.StatusBadRequest {
		t.Fatalf("256 バイトの EMM が 400 になっていません: %d", status)
	}
}

func mustNets(t *testing.T) []*net.IPNet {
	t.Helper()
	nets, err := config.ParseShareNetworks(config.DefaultShareNetworks())
	if err != nil {
		t.Fatal(err)
	}
	return nets
}

// TestShareServerStalledBody は、本文を途中で止めた接続が切られることを確かめます。
// ヘッダーを送ったあと 10 バイトだけ送って止めると、ReadTimeout で切断されます。
// 切るときに 500 が返ることもありますが、応答の有無は問いません。切られることが大事です。
func TestShareServerStalledBody(t *testing.T) {
	srv := New(0, mustNets(t), fakeLocal{}, func(format string, args ...any) {})
	srv.readTimeout = 200 * time.Millisecond
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	conn, err := net.Dial("tcp", "127.0.0.1:"+sharePort(srv))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = conn.Write([]byte("POST /denpa/card/ecm HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Type: application/octet-stream\r\nContent-Length: 255\r\n\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Write(make([]byte, 10))
	if err != nil {
		t.Fatal(err)
	}
	// 切られなければ、ここで 5 秒の締め切りにかかります。
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(conn)
	for {
		if _, err := br.ReadByte(); err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				t.Fatal("本文を途中で止めた接続が切られませんでした")
			}
			return
		}
	}
}

// TestLogRefusedCap は、断った記録が上限を超えないことを確かめます。
func TestLogRefusedCap(t *testing.T) {
	srv := New(0, mustNets(t), fakeLocal{}, func(format string, args ...any) {})
	for i := 0; i < maxRefused+500; i++ {
		srv.logRefused(fmt.Sprintf("peer-%d", i), "CARD_SHARE_NETWORKS", "理由")
	}
	srv.mu.Lock()
	n := len(srv.refused)
	srv.mu.Unlock()
	if n > maxRefused {
		t.Fatalf("断った記録が %d 件になり、上限 %d を超えました", n, maxRefused)
	}
}

// TestLogRefusedExpiry は、10 分以上前の記録が捨てられることを確かめます。
func TestLogRefusedExpiry(t *testing.T) {
	srv := New(0, mustNets(t), fakeLocal{}, func(format string, args ...any) {})
	srv.mu.Lock()
	srv.refused["old/CARD_SHARE_NETWORKS"] = time.Now().Add(-11 * time.Minute)
	srv.mu.Unlock()
	srv.logRefused("new", "CARD_SHARE_NETWORKS", "理由")
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if _, ok := srv.refused["old/CARD_SHARE_NETWORKS"]; ok {
		t.Fatal("10 分以上前の記録が残っています")
	}
	if _, ok := srv.refused["new/CARD_SHARE_NETWORKS"]; !ok {
		t.Fatal("新しい記録がありません")
	}
}
