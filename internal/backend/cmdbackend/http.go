package cmdbackend

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
)

// 応答が 200 でないとき、記録に出す本文の長さ (バイト)。
const httpBodyLog = 200

// newHTTPClient は url の機種が使う HTTP クライアント。ヘッダーが返るまでは firstTS 待ちと同じ時間だけ待ち、
// 本文 (TS) は止めるまで読み続けるので、全体の時間切れは付けない。
// リダイレクトは追わない (設定した接続先とは別のホストへ飛ばされないように)。環境のプロキシも使わない。
func newHTTPClient(firstTS time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			ResponseHeaderTimeout: firstTS,
			DisableKeepAlives:     true, // 選局のたびに新しく繋ぐ。nasne は 1 回に 1 つの要求だけ
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// startHTTPSession は url に GET して、200 の本文をパイプに流す。子プロセスと同じ session の形にして、
// 最初の TS を待つ・止める・読み出しは、cmd と同じ仕組みを使う。
func (t *tuner) startHTTPSession(url string) (*session, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("%s: パイプを作れません (%v)", t.spec.Name, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		pr.Close()
		pw.Close()
		return nil, fmt.Errorf("%s: URL を作れません (%v)", t.spec.Name, err)
	}
	s := &session{
		r:       pr,
		grace:   t.b.cfg.StopGrace,
		cancel:  cancel,
		firstCh: make(chan firstResult, 1),
		done:    make(chan struct{}),
		abort:   make(chan struct{}),
	}
	go func() {
		defer func() {
			pw.Close() // 読み手が EOF になる
			close(s.done)
		}()
		resp, err := t.b.http.Do(req)
		if err != nil {
			if ctx.Err() == nil {
				s.httpErr = err
			}
			return
		}
		defer resp.Body.Close()
		s.httpStatus = resp.StatusCode
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, httpBodyLog))
			s.httpBody = strings.TrimSpace(string(b))
			return
		}
		if _, err := io.Copy(pw, resp.Body); err != nil && ctx.Err() == nil {
			s.httpErr = err
		}
	}()
	go func() {
		buf := make([]byte, firstReadSize)
		n, err := pr.Read(buf)
		s.firstCh <- firstResult{data: buf[:n], err: err}
	}()
	return s, nil
}

// httpExited は TS が来る前に HTTP が終わったとき。503 は使用中 (409)、404 は同期しなかった、
// 繋がらない・ほかのステータスは、使用中でない失敗にして、理由を伝える。
func (t *tuner) httpExited(s *session, typ string) error {
	switch {
	case s.httpStatus == http.StatusServiceUnavailable:
		t.logf("HTTP 503 (使用中)")
		return &backend.StatusError{
			Code: 409,
			Msg:  fmt.Sprintf("%s のチューナーに空きがありません", typ),
			Err:  backend.ErrBusy,
		}
	case s.httpStatus == http.StatusNotFound:
		t.logf("HTTP 404 (電波なし・サービスなし)")
		return fmt.Errorf("%s: %w", t.spec.Name, backend.ErrNoLock)
	case s.httpStatus != 0:
		if s.httpBody != "" {
			t.logf("HTTP %d: %s", s.httpStatus, s.httpBody)
		} else {
			t.logf("HTTP %d", s.httpStatus)
		}
		return fmt.Errorf("%s: 機材が HTTP %d を返しました (使用中でも電波なしでもありません。機材のログを見てください)", t.spec.Name, s.httpStatus)
	case s.httpErr != nil:
		t.logf("HTTP で繋がりません: %v", s.httpErr)
		return fmt.Errorf("%s: 機材に HTTP で繋がりません (%v)", t.spec.Name, s.httpErr)
	}
	// 200 で本文が空のまま閉じた
	t.logf("HTTP 200 で、TS を出さずに閉じました")
	return fmt.Errorf("%s: %w", t.spec.Name, backend.ErrNoLock)
}
