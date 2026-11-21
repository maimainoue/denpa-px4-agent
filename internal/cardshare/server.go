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
	"time"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/b25"
)

// LocalCard は配る側が手元のカードに求めるものです。card.Manager が満たします。
// EMMNow は EMM をすぐに送って返り値を返すもので、順番待ちに入れる EMM とは別です。
// 配る側は、denpa の付属のエージェントと同じく、カードの返り値そのものを返します。
type LocalCard interface {
	Info() (b25.Info, error)
	ECM(ecm []byte) (b25.ECMResult, error)
	Confirm(ctx context.Context, verification []byte, mjd int) (int, error)
	EMMNow(payload []byte) (int, error)
}

// Server は鍵を配る側です。CARD_SHARE_PORT を書いたときだけ開きます。
// 平文の HTTP で、鍵を配る API にしか応答しません。AGENT_HOST に関わらず
// 全部のインターフェースで待ち受け、アドレスの範囲で絞ります。
// TTL と往復時間は PR 4 で入るので、それまでは CARD_SHARE_NETWORKS の判定だけです。
// 信頼できる LAN の中でだけ使ってください。
type Server struct {
	port   int
	policy *Policy
	local  LocalCard
	logf   func(format string, args ...any)

	mu      sync.Mutex
	refused map[string]time.Time // 断った記録 (相手+理由 → 最後に出した時刻)。10 分に 1 回だけ出します。
	srv     *http.Server
	ln      net.Listener
	closed  bool
}

// New は鍵を配るサーバを作ります。nets は config.ParseShareNetworks の結果です。
func New(port int, nets []*net.IPNet, local LocalCard, logf func(format string, args ...any)) *Server {
	if logf == nil {
		logf = func(format string, args ...any) {}
	}
	return &Server{
		port: port, policy: NewPolicy(nets), local: local, logf: logf,
		refused: map[string]time.Time{},
	}
}

type connKey struct{}

// Start は待ち受けを始めます。1 つのソケットで IPv4 と IPv6 の両方を受けます。
func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/denpa/card/init", s.handleInit)
	mux.HandleFunc("/denpa/card/ecm", s.handleECM)
	mux.HandleFunc("/denpa/card/confirm", s.handleConfirm)
	mux.HandleFunc("/denpa/card/emm", s.handleEMM)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSONError(w, http.StatusNotFound, "鍵を配っていません")
	})
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", s.port))
	if err != nil {
		return fmt.Errorf("鍵を配る待ち受けが作れません (%v)", err)
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	s.srv = &http.Server{
		Handler: s.checkPeer(mux),
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, connKey{}, c)
		},
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = s.srv.Serve(ln) }()
	s.logf("鍵を配ります (ポート %d)", s.port)
	return nil
}

// Addr は待ち受けているアドレス (host:port) です。ポート 0 で開いたときに使います。
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Close は待ち受けを止めます。
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.srv == nil {
		return nil
	}
	return s.srv.Close()
}

// checkPeer は相手のアドレスを確かめてから、鍵を配る API に通します。
// 接続の RemoteAddr を policy.Allowed に渡すので、ゾーン付きのリンクローカルも正しく見ます。
func (s *Server) checkPeer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := r.Context().Value(connKey{}).(net.Conn)
		var addr net.Addr
		if c != nil {
			addr = c.RemoteAddr()
		}
		if !s.policy.Allowed(addr) {
			host := r.RemoteAddr
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			reason := fmt.Sprintf("%s は鍵を配る範囲 (CARD_SHARE_NETWORKS) の外です", host)
			s.logRefused(host, "CARD_SHARE_NETWORKS", reason)
			writeJSONError(w, http.StatusForbidden, reason)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// logRefused は断ったことを記録に残します。同じ相手を同じ理由で断ったものは 10 分に 1 回です。
func (s *Server) logRefused(peer, rule, reason string) {
	key := peer + "/" + rule
	s.mu.Lock()
	last, ok := s.refused[key]
	if !ok || time.Since(last) >= 10*time.Minute {
		s.refused[key] = time.Now()
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
			writeJSONError(w, http.StatusRequestEntityTooLarge, "ECM が大きすぎます (255 バイトまで)")
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
			writeJSONError(w, http.StatusRequestEntityTooLarge, "契約確認データが大きすぎます (253 バイトまで)")
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
			writeJSONError(w, http.StatusRequestEntityTooLarge, "EMM が大きすぎます (255 バイトまで)")
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
