package cardshare

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/b25"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/card"
)

// ShareRefused は、配る側が応答したが求めたものを返さなかったときの理由です。
// HTTP の状態を持ちます。
type ShareRefused struct {
	Status  int
	Message string
}

func (e *ShareRefused) Error() string { return e.Message }

// RemoteCard はカードを持っていない拠点です。ECM を送って鍵だけ受け取ります。
// 繋ぐのは LAN の中だけです。TTL は PR 4 で入るので、ここでは付けていません。
type RemoteCard struct {
	url  string
	logf func(format string, args ...any)

	http *http.Client

	mu         sync.Mutex
	init       *b25.Info
	emmMissing bool
}

// NewRemoteCard は貰う側のカードを作ります。url は配る側の CARD_SHARE_PORT の
// ポートを指す http://ホスト:ポート です。
func NewRemoteCard(url string, logf func(format string, args ...any)) *RemoteCard {
	if logf == nil {
		logf = func(format string, args ...any) {}
	}
	r := &RemoteCard{url: strings.TrimRight(url, "/"), logf: logf}
	r.http = &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: 5 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout: 5 * time.Second,
		},
		Timeout: 10 * time.Second,
	}
	return r
}

// URL は配る側の URL です。画面に出します。
func (r *RemoteCard) URL() string { return r.url }

// Init はカードの定数です。1 回受け取れば変わらないので控えておきます。
// 受け取れなかったら次に要ったときにまた問い合わせます。
func (r *RemoteCard) Init() (b25.Info, error) {
	r.mu.Lock()
	if r.init != nil {
		init := *r.init
		r.mu.Unlock()
		return init, nil
	}
	r.mu.Unlock()
	return r.Check()
}

// Check は配る側とやり取りできるかです。控えている定数を使わずに問い合わせ直します。
func (r *RemoteCard) Check() (b25.Info, error) {
	body, err := r.send(http.MethodGet, "init", nil, "カードの情報")
	if err != nil {
		return b25.Info{}, err
	}
	init, err := ReadInit(body)
	if err != nil {
		return b25.Info{}, err
	}
	r.mu.Lock()
	r.init = &init
	r.mu.Unlock()
	return init, nil
}

// Ecm は ECM 1 つを送って鍵を受け取ります。
func (r *RemoteCard) Ecm(ecm []byte) (b25.ECMResult, error) {
	if len(ecm) > MaxECM {
		return b25.ECMResult{}, fmt.Errorf("ECM が大きすぎます (%d バイト。255 バイトまで)", len(ecm))
	}
	body, err := r.post("ecm", ecm, "鍵")
	if err != nil {
		return b25.ECMResult{}, err
	}
	return ReadECM(body)
}

// Confirm は契約確認です。verification は 1〜253 バイト、dateMJD は MJD です。
func (r *RemoteCard) Confirm(verification []byte, dateMJD uint16) (uint16, error) {
	if err := CheckConfirm(verification); err != nil {
		return 0, err
	}
	payload := make([]byte, 2+len(verification))
	binary.BigEndian.PutUint16(payload, dateMJD)
	copy(payload[2:], verification)
	body, err := r.post("confirm", payload, "契約確認")
	if err != nil {
		return 0, err
	}
	return ReadCode(body, "契約確認")
}

// Emm は契約の更新 (EMM) を配る側に渡し、配る側のカードの返り値を返します。
// 配る側が古くて /denpa/card/emm を持っていない (404) なら、記録に 1 回だけ出し、
// それからは送らずに card.ErrEmmUnsupported を返します。鍵と契約確認はそのまま使います。
func (r *RemoteCard) Emm(payload []byte) (uint16, error) {
	r.mu.Lock()
	missing := r.emmMissing
	r.mu.Unlock()
	if missing {
		return 0, card.ErrEmmUnsupported
	}
	body, err := r.post("emm", payload, "契約の更新 (EMM) の返り値")
	if err != nil {
		var refused *ShareRefused
		if errors.As(err, &refused) && refused.Status == http.StatusNotFound {
			r.mu.Lock()
			if !r.emmMissing {
				r.emmMissing = true
				r.logf("%v (%s)", card.ErrEmmUnsupported, r.url)
			}
			r.mu.Unlock()
			return 0, card.ErrEmmUnsupported
		}
		return 0, err
	}
	return ReadCode(body, "EMM ")
}

// post は /denpa/card/<path> にバイト列を POST して中身を受け取ります。
func (r *RemoteCard) post(path string, body []byte, what string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodPost, r.url+"/denpa/card/"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	return r.sendRequest(req, what)
}

// send は GET などで中身を受け取ります。
func (r *RemoteCard) send(method, path string, body []byte, what string) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, r.url+"/denpa/card/"+path, reader)
	if err != nil {
		return nil, err
	}
	return r.sendRequest(req, what)
}

// sendRequest は送って中身を受け取ります。受け取れなければ、何が起きたかの分かる文で返します。
func (r *RemoteCard) sendRequest(req *http.Request, what string) ([]byte, error) {
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, r.unreachable(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("鍵を配る拠点から%sを受け取れません (%v)", what, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return body, nil
	}
	return nil, &ShareRefused{Status: resp.StatusCode, Message: refused(r.url, resp.StatusCode, body, what)}
}

// refused は配る側は応答したが、求めたものを返さなかったときの文です。
func refused(url string, status int, body []byte, what string) string {
	reason := errorReason(body)
	switch status {
	case http.StatusForbidden:
		if reason != "" {
			return fmt.Sprintf("鍵を配る拠点に断られました: %s", reason)
		}
		return "鍵を配る拠点に断られました: 理由は返っていません"
	case http.StatusNotFound:
		// CARD_URL が鍵を配る API の無い所を指している。普通の API のポート (TLS だけ) なら、ここへは来ずに繋がらない
		return fmt.Sprintf("%s では鍵を配っていません (404)。CARD_URL には配る側の CARD_SHARE_PORT のポートを書きます", url)
	default:
		if reason != "" {
			return fmt.Sprintf("鍵を配る拠点から%sを受け取れません (%d: %s)", what, status, reason)
		}
		return fmt.Sprintf("鍵を配る拠点から%sを受け取れません (%d)", what, status)
	}
}

// errorReason は配る側が返した {"error": "..."} です。
func errorReason(body []byte) string {
	var parsed struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	return parsed.Error
}

// unreachable は配る側に繋がらなかったときの文です。
func (r *RemoteCard) unreachable(err error) error {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		var sysErr *os.SyscallError
		if errors.As(opErr.Err, &sysErr) {
			if errors.Is(sysErr.Err, syscall.ECONNREFUSED) {
				return fmt.Errorf("鍵を配る拠点に繋がりません (%v)。配る側で CARD_SHARE_PORT を開いているか確かめてください", err)
			}
			if errors.Is(sysErr.Err, syscall.ETIMEDOUT) ||
				errors.Is(sysErr.Err, syscall.EHOSTUNREACH) ||
				errors.Is(sysErr.Err, syscall.ENETUNREACH) {
				return fmt.Errorf("鍵を配る拠点に繋がりません (%v)。ルーターを越えた先や LAN の外には届きません", err)
			}
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("鍵を配る拠点から応答がありません")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("鍵を配る拠点から応答がありません")
	}
	return fmt.Errorf("鍵を配る拠点に繋がりません: %v", err)
}
