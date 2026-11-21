package cardshare

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/b25"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/card"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/config"
)

// TestRemoteCardRoundTrip は、貰う側と配る側の往復を確かめます。
func TestRemoteCardRoundTrip(t *testing.T) {
	srv := startShareServer(t, config.DefaultShareNetworks())
	remote := NewRemoteCard(shareURL(srv), 1, func(format string, args ...any) {})

	init, err := remote.Init()
	if err != nil {
		t.Fatal(err)
	}
	if init.CASystemID != 0x0005 || len(init.IDs) == 0 || init.IDs[0] != (b25.CardID{0x00, 0x01, 0x23, 0x45, 0x67, 0x89}) {
		t.Fatalf("init が違います: %+v", init)
	}
	if init.SystemKey[0] != 0x10 || init.CBCInit[0] != 0xa0 {
		t.Fatalf("init の中身が違います: %+v", init)
	}

	answer, err := remote.Ecm([]byte{0x00, 0x01, 0x02, 0x03, 0x04, 0xaa, 0xbb, 0xcc})
	if err != nil {
		t.Fatal(err)
	}
	if answer.Code != 0x0200 || answer.Odd[0] != 0x11 || answer.Even[0] != 0x22 {
		t.Fatalf("ECM の答えが違います: %+v", answer)
	}

	code, err := remote.Confirm([]byte{0xe0, 0x00, 0x01, 0x02}, 0x1234)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0x0800 {
		t.Fatalf("契約確認の返り値が違います: %#04x", code)
	}

	code, err = remote.Emm([]byte{0x36, 0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b})
	if err != nil {
		t.Fatal(err)
	}
	if code != 0x2100 {
		t.Fatalf("EMM の返り値が違います: %#04x", code)
	}

	// 問い合わせ直しても同じ定数が返ります。
	init2, err := remote.Check()
	if err != nil {
		t.Fatal(err)
	}
	if init2.CASystemID != init.CASystemID {
		t.Fatalf("Check の結果が違います: %+v", init2)
	}

	// 配る側が落ちても、控えている定数は返ります。
	_ = srv.Close()
	init3, err := remote.Init()
	if err != nil {
		t.Fatal(err)
	}
	if init3.CASystemID != init.CASystemID {
		t.Fatalf("控えている定数が違います: %+v", init3)
	}
}

// TestRemoteCardEmmUnsupported は、配る側が古くて /emm を持っていないとき、
// 2 回目からは送らずに ErrEmmUnsupported を返すことを確かめます。
func TestRemoteCardEmmUnsupported(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.NotFound(w, r)
	}))
	defer srv.Close()

	remote := NewRemoteCard(srv.URL, 1, func(format string, args ...any) {})
	if _, err := remote.Emm(make([]byte, 13)); !errors.Is(err, card.ErrEmmUnsupported) {
		t.Fatalf("1 回目が ErrEmmUnsupported ではありません: %v", err)
	}
	if calls != 1 {
		t.Fatalf("1 回目に %d 回送っています", calls)
	}
	if _, err := remote.Emm(make([]byte, 13)); !errors.Is(err, card.ErrEmmUnsupported) {
		t.Fatalf("2 回目が ErrEmmUnsupported ではありません: %v", err)
	}
	if calls != 1 {
		t.Fatalf("2 回目に送っています (%d 回)", calls)
	}
}

// TestRemoteCardRefused は、配る側が断ったときの理由を確かめます。
func TestRemoteCardRefused(t *testing.T) {
	// 403 は理由付きで返ります。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error": "範囲の外です"}`)
	}))
	defer srv.Close()
	remote := NewRemoteCard(srv.URL, 1, func(format string, args ...any) {})
	_, err := remote.Check()
	var refused *ShareRefused
	if !errors.As(err, &refused) || refused.Status != http.StatusForbidden {
		t.Fatalf("403 が ShareRefused になりません: %v", err)
	}
	if !strings.Contains(refused.Message, "範囲の外です") {
		t.Fatalf("断った理由が入っていません: %s", refused.Message)
	}

	// 404 は CARD_URL の書き方を案内します。
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv2.Close()
	remote2 := NewRemoteCard(srv2.URL, 1, func(format string, args ...any) {})
	_, err = remote2.Check()
	if !errors.As(err, &refused) || refused.Status != http.StatusNotFound {
		t.Fatalf("404 が ShareRefused になりません: %v", err)
	}
	if !strings.Contains(refused.Message, "CARD_URL") {
		t.Fatalf("CARD_URL の案内が入っていません: %s", refused.Message)
	}
}

// TestRemoteCardUnreachable は、配る側が閉じているとき、開いているか確かめる案内が出ることを確かめます。
func TestRemoteCardUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	remote := NewRemoteCard("http://"+addr, 1, func(format string, args ...any) {})
	_, err = remote.Check()
	if err == nil || !strings.Contains(err.Error(), "CARD_SHARE_PORT を開いているか確かめてください") {
		t.Fatalf("繋がらないときの案内が出ません: %v", err)
	}
}

// TestRemoteCardResponseTooLarge は、配る側の応答が大きすぎるとき、読むのをやめることを確かめます。
func TestRemoteCardResponseTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, maxResponseBody+1))
	}))
	defer srv.Close()
	remote := NewRemoteCard(srv.URL, 1, func(format string, args ...any) {})
	_, err := remote.Check()
	if err == nil || !strings.Contains(err.Error(), "大きすぎます") {
		t.Fatalf("大きすぎる応答がエラーになりません: %v", err)
	}
}

// TestRemoteCardNoRedirect は、配る側が別のホストへ向けても、追わないことを確かめます。
func TestRemoteCardNoRedirect(t *testing.T) {
	otherHits := 0
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHits++
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	remote := NewRemoteCard(srv.URL, 1, func(format string, args ...any) {})
	_, err := remote.Ecm([]byte{0x00, 0x01, 0x02, 0x03, 0x04, 0xaa, 0xbb, 0xcc})
	if err == nil {
		t.Fatal("307 がエラーになりません")
	}
	if otherHits != 0 {
		t.Fatalf("別のホストへ %d 回送っています", otherHits)
	}
}

// TestRemoteCardDownBackoff は、配る側に繋がらなかったあと、しばらく試さないことを確かめます。
func TestRemoteCardDownBackoff(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// 応答せずに切ります。繋がらなかったことになります。
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("Hijack できません")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}))
	defer srv.Close()
	remote := NewRemoteCard(srv.URL, 1, func(format string, args ...any) {})
	remote.downBackoff = 200 * time.Millisecond
	ecm := []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0xaa, 0xbb, 0xcc}
	if _, err := remote.Ecm(ecm); err == nil {
		t.Fatal("1 回目がエラーになりません")
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("1 回目に %d 回送っています", n)
	}
	// すぐの 2 回目は、試さずに前回の理由を返します。
	start := time.Now()
	if _, err := remote.Ecm(ecm); err == nil {
		t.Fatal("2 回目がエラーになりません")
	}
	if took := time.Since(start); took > 100*time.Millisecond {
		t.Fatalf("2 回目に %v かかっています", took)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("2 回目に送っています (%d 回)", n)
	}
	// 間が過ぎれば、また試します。
	time.Sleep(250 * time.Millisecond)
	if _, err := remote.Ecm(ecm); err == nil {
		t.Fatal("3 回目がエラーになりません")
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("3 回目に %d 回送っています", n)
	}
}
