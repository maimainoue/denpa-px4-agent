package cardshare

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/b25"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/card"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/config"
)

// TestRemoteCardRoundTrip は、貰う側と配る側の往復を確かめます。
func TestRemoteCardRoundTrip(t *testing.T) {
	srv := startShareServer(t, config.DefaultShareNetworks())
	remote := NewRemoteCard(shareURL(srv), func(format string, args ...any) {})

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

	remote := NewRemoteCard(srv.URL, func(format string, args ...any) {})
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
	remote := NewRemoteCard(srv.URL, func(format string, args ...any) {})
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
	remote2 := NewRemoteCard(srv2.URL, func(format string, args ...any) {})
	_, err = remote2.Check()
	if !errors.As(err, &refused) || refused.Status != http.StatusNotFound {
		t.Fatalf("404 が ShareRefused になりません: %v", err)
	}
	if !strings.Contains(refused.Message, "CARD_URL") {
		t.Fatalf("CARD_URL の案内が入っていません: %s", refused.Message)
	}
}
