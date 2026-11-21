package cardshare

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"syscall"
	"time"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/card"
)

// Server は鍵を配る側です。CARD_SHARE_PORT を書いたときだけ開きます。
// 平文の HTTP で、鍵を配る API にしか応答しません。AGENT_HOST に関わらず
// 全部のインターフェースで待ち受け、アドレスの範囲と TTL と往復時間で絞ります。
// 信頼できる LAN の中でだけ使ってください。
type Server struct {
	port   int
	ttl    int
	maxRTT time.Duration
	policy *Policy
	local  card.LocalCard
	// wrap は待ち受けを包む関数です。app は wrapListener (accept4 の無い古いカーネルで
	// accept に切り替える) を渡します。nil なら、そのまま使います。
	wrap func(net.Listener) net.Listener
	logf func(format string, args ...any)

	// 待ち受けのタイムアウト。New が既定値 (読み 15 秒・書き 10 秒・遊び 60 秒) を入れます。
	// 貰う側は応答を 10 秒まで待つので、読みはそれより少し長くしています。
	// テストでは短くして上書きします。
	readTimeout  time.Duration
	writeTimeout time.Duration
	idleTimeout  time.Duration

	// sem は同時に受ける要求の数の上限です。いっぱいのときは 503 を返します。
	sem chan struct{}

	mu        sync.Mutex
	refused   map[string]time.Time // 断った記録 (相手+理由 → 最後に出した時刻)。10 分に 1 回だけ出します。
	servers   []*http.Server
	listeners []net.Listener
	closed    bool
}

// refused の記録の上限 (件)。超えたら古いものから捨てます。放っておくと増え続けます。
const maxRefused = 1024

// maxConcurrentRequests は同時に受ける要求の数の上限です。カードは 1 つなので、
// LAN からの多数の要求が手元の視聴の ECM を遅らせないようにします。
const maxConcurrentRequests = 4

// New は鍵を配るサーバを作ります。nets は config.ParseShareNetworks の結果です。
// wrap は待ち受けを包む関数で、app は wrapListener を渡します。nil ならそのまま使います。
// ttl は CARD_TTL (1〜8)、maxRTTMs は往復時間の上限 (ミリ秒)。0 なら見ません。
func New(port int, nets []*net.IPNet, local card.LocalCard, wrap func(net.Listener) net.Listener, ttl int, maxRTTMs int, logf func(format string, args ...any)) *Server {
	if logf == nil {
		logf = func(format string, args ...any) {}
	}
	return &Server{
		port: port, ttl: ttl, maxRTT: time.Duration(maxRTTMs) * time.Millisecond,
		policy: NewPolicy(nets), local: local, wrap: wrap, logf: logf,
		readTimeout: 15 * time.Second, writeTimeout: 10 * time.Second, idleTimeout: 60 * time.Second,
		sem:     make(chan struct{}, maxConcurrentRequests),
		refused: map[string]time.Time{},
	}
}

type connKey struct{}

// Start は待ち受けを始めます。IPv4 と IPv6 は別々のソケットで待ち受けます
// (1 つで両方を受けると、IPv4 の相手には IPv6 の hop limit が効かないためです)。
// IPv6 が使えなければ、警告を出して IPv4 だけで続けます。
func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/denpa/card/init", s.handleInit)
	mux.HandleFunc("/denpa/card/ecm", s.handleECM)
	mux.HandleFunc("/denpa/card/confirm", s.handleConfirm)
	mux.HandleFunc("/denpa/card/emm", s.handleEMM)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSONError(w, http.StatusNotFound, "鍵を配っていません")
	})
	handler := s.checkPeer(mux)

	started := false
	var listeners []net.Listener
	var servers []*http.Server
	for _, network := range []string{"tcp4", "tcp6"} {
		addr := "0.0.0.0"
		if network == "tcp6" {
			addr = "[::]"
		}
		lc := net.ListenConfig{
			Control: func(nw, address string, c syscall.RawConn) error {
				return setSocketTTL(c, nw, s.ttl)
			},
		}
		ln, err := lc.Listen(context.Background(), network, fmt.Sprintf("%s:%d", addr, s.port))
		if err != nil {
			if network == "tcp6" {
				s.logf("IPv6 で鍵を配る待ち受けが作れません (%v)。IPv4 だけで配ります", err)
				continue
			}
			return fmt.Errorf("鍵を配る待ち受けが作れません (%v)", err)
		}
		// 渡された関数で待ち受けを包む。app は wrapListener (accept4 の無い古いカーネルで
		// accept に切り替える) を渡す。nil なら、そのまま使う。
		if s.wrap != nil {
			ln = s.wrap(ln)
		}
		wrapped := &ttlListener{Listener: ln, network: network, ttl: s.ttl}
		srv := &http.Server{
			Handler: handler,
			ConnContext: func(ctx context.Context, c net.Conn) context.Context {
				return context.WithValue(ctx, connKey{}, c)
			},
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       s.readTimeout,
			WriteTimeout:      s.writeTimeout,
			IdleTimeout:       s.idleTimeout,
		}
		listeners = append(listeners, wrapped)
		servers = append(servers, srv)
		started = true
	}
	if !started {
		return fmt.Errorf("鍵を配る待ち受けが作れませんでした")
	}
	s.mu.Lock()
	if s.closed {
		// 公開する前に閉じられていたら、作った待ち受けを閉じてエラーを返します。
		// このまま公開すると、以後 Close しても待ち受けが残ります。
		s.mu.Unlock()
		for _, ln := range listeners {
			ln.Close()
		}
		return fmt.Errorf("鍵を配る待ち受けは閉じています")
	}
	s.listeners = listeners
	s.servers = servers
	s.mu.Unlock()
	for i := range servers {
		go func() { _ = servers[i].Serve(listeners[i]) }()
	}
	s.logf("鍵を配ります (ポート %d、TTL %d)", s.port, s.ttl)
	return nil
}

// Addr は待ち受けているアドレス (host:port) です。ポート 0 で開いたときに使います。
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.listeners) == 0 {
		return ""
	}
	return s.listeners[0].Addr().String()
}

// Close は待ち受けを止めます。
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var first error
	for _, srv := range s.servers {
		if err := srv.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// ttlListener は受けた接続に TTL を付けてから渡す待ち受けです。
// 付けられなければ、何も返さずに切ります。
type ttlListener struct {
	net.Listener
	network string
	ttl     int
}

func (l *ttlListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tc, ok := c.(*net.TCPConn)
	if !ok {
		// TCP でなければ TTL を付けられないので、切ります。
		// 付けずに通すと、「付けられなければ切る」という作りと食い違います。
		_ = c.Close()
		return nil, tempError{fmt.Errorf("TCP の接続ではありません")}
	}
	raw, err := tc.SyscallConn()
	if err != nil {
		_ = c.Close()
		return nil, tempError{err}
	}
	if err := setSocketTTL(raw, l.network, l.ttl); err != nil {
		_ = c.Close()
		return nil, tempError{err}
	}
	if err := checkSocketTTL(raw, l.network, l.ttl); err != nil {
		_ = c.Close()
		return nil, tempError{err}
	}
	return c, nil
}

// tempError は一時的な受け取りの失敗です。http.Server は待ち受けを続けます。
type tempError struct{ err error }

func (e tempError) Error() string   { return e.err.Error() }
func (e tempError) Timeout() bool   { return false }
func (e tempError) Temporary() bool { return true }

// checkPeer は相手のアドレスと往復時間と同時の要求の数を確かめてから、鍵を配る API に通します。
// 接続の RemoteAddr を policy.Allowed に渡すので、ゾーン付きのリンクローカルも正しく見ます。
func (s *Server) checkPeer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := r.Context().Value(connKey{}).(net.Conn)
		var addr net.Addr
		if c != nil {
			addr = c.RemoteAddr()
		}
		host := r.RemoteAddr
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !s.policy.Allowed(addr) {
			reason := fmt.Sprintf("%s は鍵を配る範囲 (CARD_SHARE_NETWORKS) の外です", host)
			s.logRefused(host, "CARD_SHARE_NETWORKS", reason)
			writeJSONError(w, http.StatusForbidden, reason)
			return
		}
		select {
		case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
		default:
			reason := "同時に受ける要求の数が上限を超えています"
			s.logRefused(host, "同時要求の上限", reason)
			writeJSONError(w, http.StatusServiceUnavailable, reason)
			return
		}
		if s.maxRTT > 0 {
			rtt, err := s.peerRTT(r)
			if err != nil {
				reason := fmt.Sprintf("往復時間が測れません (%v)", err)
				s.logRefused(host, "CARD_SHARE_MAX_RTT", reason)
				writeJSONError(w, http.StatusForbidden, reason)
				return
			}
			if rtt > s.maxRTT {
				reason := fmt.Sprintf("往復時間が上限を超えています (%s > %s)", rtt, s.maxRTT)
				s.logRefused(host, "CARD_SHARE_MAX_RTT", reason)
				writeJSONError(w, http.StatusForbidden, reason)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// peerRTT は相手との往復時間を測ります。
func (s *Server) peerRTT(r *http.Request) (time.Duration, error) {
	c, ok := r.Context().Value(connKey{}).(net.Conn)
	if !ok {
		return 0, fmt.Errorf("接続が分かりません")
	}
	tc, ok := c.(*net.TCPConn)
	if !ok {
		return 0, fmt.Errorf("TCP の接続ではありません")
	}
	raw, err := tc.SyscallConn()
	if err != nil {
		return 0, err
	}
	return measureRTT(raw)
}

// logRefused は断ったことを記録に残します。同じ相手を同じ理由で断ったものは 10 分に 1 回です。
// 10 分以上前の記録は捨て、件数が上限 (maxRefused) を超えたら古いものから捨てます。
// 掃除がないと、異なる相手を断り続けるだけで記録が増え続けます。
func (s *Server) logRefused(peer, rule, reason string) {
	key := peer + "/" + rule
	s.mu.Lock()
	now := time.Now()
	oldestKey := ""
	var oldest time.Time
	for k, last := range s.refused {
		if now.Sub(last) >= 10*time.Minute {
			delete(s.refused, k)
			continue
		}
		if oldestKey == "" || last.Before(oldest) {
			oldestKey, oldest = k, last
		}
	}
	last, ok := s.refused[key]
	if !ok || now.Sub(last) >= 10*time.Minute {
		if !ok && len(s.refused) >= maxRefused {
			delete(s.refused, oldestKey)
		}
		s.refused[key] = now
		s.mu.Unlock()
		s.logf("鍵を配るのを断りました: %s (%s) %s", peer, rule, reason)
		return
	}
	s.mu.Unlock()
}

func (s *Server) handleInit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method が違います")
		return
	}
	init, err := s.local.Info()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOctets(w, PackInit(init))
}

func (s *Server) handleECM(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method が違います")
		return
	}
	body, err := ReadUpTo(r.Body, MaxECM)
	if err != nil {
		if errors.Is(err, ErrTooLarge) {
			writeJSONError(w, http.StatusBadRequest, "ECM が大きすぎます (255 バイトまで)")
		} else {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	if len(body) == 0 {
		writeJSONError(w, http.StatusBadRequest, "ECM がありません")
		return
	}
	answer, err := s.local.ECM(body)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOctets(w, PackECM(answer))
}

func (s *Server) handleConfirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method が違います")
		return
	}
	body, err := ReadUpTo(r.Body, 2+MaxVerification)
	if err != nil {
		if errors.Is(err, ErrTooLarge) {
			writeJSONError(w, http.StatusBadRequest, "契約確認データが大きすぎます (253 バイトまで)")
		} else {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	if len(body) < 3 {
		writeJSONError(w, http.StatusBadRequest, "契約確認データの長さがおかしいです")
		return
	}
	dateMJD := binary.BigEndian.Uint16(body[0:2])
	verification := body[2:]
	if err := CheckConfirm(verification); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	code, err := s.local.Confirm(r.Context(), verification, int(dateMJD))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOctets(w, PackCode(uint16(code)))
}

func (s *Server) handleEMM(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method が違います")
		return
	}
	body, err := ReadUpTo(r.Body, 255)
	if err != nil {
		if errors.Is(err, ErrTooLarge) {
			writeJSONError(w, http.StatusBadRequest, "EMM が大きすぎます (255 バイトまで)")
		} else {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	if len(body) < 13 || len(body) > 255 {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("EMM の長さがおかしいです (%d バイト)", len(body)))
		return
	}
	// カードの返り値そのものを返します。キューに入れただけでは 0x2100 を返しません。
	// 付属のエージェントはこの返り値を見て、受け入れた EMM はもう送らず、
	// 受け入れなかった EMM は次に流れてきたら送り直すので、偽りの 0x2100 は EMM を失います。
	code, err := s.local.EMMNow(body)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOctets(w, PackCode(uint16(code)))
}

func writeOctets(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]string{"error": message})
}
