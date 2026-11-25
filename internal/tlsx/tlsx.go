// Package tlsx は denpa と TLS で組む手順 (仕様書 11)。
package tlsx

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Fingerprint は DER の SHA-256 (小文字 16 進 64 字)。
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// Identity は自分の鍵と証明書、組んだ相手の指紋、合言葉。
type Identity struct {
	dir   string
	Cert  tls.Certificate
	Print string // 自分の指紋
	token string

	mu      sync.Mutex
	clients map[string]bool

	logf func(string, ...any) // 組んだことの記録 (nil なら出さない)
}

// Load は tls/ の鍵と証明書を読む。無ければ作り、作り直さない。
func Load(dir, tokenEnv string, warn func(string, ...any)) (*Identity, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	keyPath, crtPath := filepath.Join(dir, "agent.key"), filepath.Join(dir, "agent.crt")
	if _, err := os.Stat(keyPath); err != nil {
		if err := create(keyPath, crtPath); err != nil {
			return nil, err
		}
	}
	cert, err := tls.LoadX509KeyPair(crtPath, keyPath)
	if err != nil {
		return nil, err
	}
	id := &Identity{dir: dir, Cert: cert, Print: Fingerprint(cert.Certificate[0]), clients: map[string]bool{}, logf: warn}

	id.token = tokenEnv
	if id.token == "" {
		tp := filepath.Join(dir, "pair-token")
		b, err := os.ReadFile(tp)
		if err != nil {
			raw := make([]byte, 32)
			rand.Read(raw)
			b = []byte(base64.RawURLEncoding.EncodeToString(raw))
			if err := os.WriteFile(tp, b, 0o600); err != nil {
				return nil, err
			}
		}
		id.token = strings.TrimSpace(string(b))
	}
	if len(id.token) < 32 && warn != nil {
		warn("合言葉が 32 字未満です。推測されにくい長さにしてください")
	}
	if data, err := os.ReadFile(filepath.Join(dir, "clients")); err == nil {
		for _, l := range strings.Split(string(data), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				id.clients[l] = true
			}
		}
	}
	return id, nil
}

func create(keyPath, crtPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "denpa-px4-agent"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(100, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return err
	}
	return os.WriteFile(crtPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
}

// Token は合言葉。
func (id *Identity) Token() string { return id.token }

// Config は TLS の設定。クライアント証明書を必須にし、TLS の段ではどれも受ける。
func (id *Identity) Config() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{id.Cert},
		MinVersion:   tls.VersionTLS12,
		ClientAuth:   tls.RequireAnyClientCert,
		NextProtos:   []string{"http/1.1"},
	}
}

// Paired は組んだ相手か。自分の指紋も通す (--status 用)。
func (id *Identity) Paired(fp string) bool {
	id.mu.Lock()
	defer id.mu.Unlock()
	return fp == id.Print || id.clients[fp]
}

// HasClients は誰かと組んでいるか。
func (id *Identity) HasClients() bool {
	id.mu.Lock()
	defer id.mu.Unlock()
	return len(id.clients) > 0
}

func (id *Identity) save() error {
	var names []string
	for k := range id.clients {
		names = append(names, k)
	}
	return os.WriteFile(filepath.Join(id.dir, "clients"), []byte(strings.Join(names, "\n")+"\n"), 0o600)
}

func (id *Identity) proof(role, d string) string {
	m := hmac.New(sha256.New, []byte(id.token))
	m.Write([]byte("denpa-pair/" + role + "\n" + id.Print + "\n" + d))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// PeerFingerprint は接続の相手の証明書の指紋。
func PeerFingerprint(r *http.Request) string {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return ""
	}
	return Fingerprint(r.TLS.PeerCertificates[0].Raw)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

// HandlePair は POST /denpa/pair。
func (id *Identity) HandlePair(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1025))
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "本文を読めません"})
		return
	}
	if len(body) > 1024 {
		writeJSON(w, 413, map[string]any{"ok": false, "error": "本文が大きすぎます (上限 1024 バイト)"})
		return
	}
	var req struct {
		Client string `json:"client"`
		Proof  string `json:"proof"`
	}
	if json.Unmarshal(body, &req) != nil || req.Client == "" {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "client と proof が必要です"})
		return
	}
	if req.Client != PeerFingerprint(r) {
		writeJSON(w, 403, map[string]any{"ok": false, "error": "証明書が名乗りと違います"})
		return
	}
	want := id.proof("client", req.Client)
	if subtle.ConstantTimeCompare([]byte(want), []byte(req.Proof)) != 1 {
		writeJSON(w, 403, map[string]any{"ok": false, "error": "合言葉が違います"})
		return
	}
	id.mu.Lock()
	added := !id.clients[req.Client]
	id.clients[req.Client] = true
	count := len(id.clients)
	err = id.save()
	id.mu.Unlock()
	if err != nil {
		writeJSON(w, 500, map[string]any{"ok": false, "error": "clients を書けません: " + err.Error()})
		return
	}
	if added && id.logf != nil {
		fp := req.Client
		if len(fp) > 16 {
			fp = fp[:16]
		}
		id.logf("denpa (%s) と組みました", fp)
		// 断りはしない (denpa を別の機械へ移すときに、新旧が並ぶため)。取り合いの約束が崩れるので注意だけ出す
		if count > 1 {
			id.logf("ほかの denpa とも組んでいます (%d 台)。チューナーを使う denpa は 1 つにしてください", count)
		}
	}
	writeJSON(w, 200, map[string]any{"proof": id.proof("agent", req.Client)})
}

// HandleUnpair は DELETE /denpa/pair。
func (id *Identity) HandleUnpair(w http.ResponseWriter, r *http.Request) {
	id.mu.Lock()
	delete(id.clients, PeerFingerprint(r))
	err := id.save()
	id.mu.Unlock()
	if err != nil {
		writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	w.WriteHeader(204)
}

// RejectUnpaired は組んでいない相手への 403。
func RejectUnpaired(w http.ResponseWriter) {
	writeJSON(w, 403, map[string]any{
		"ok":     false,
		"error":  "この denpa とはまだ組んでいません。denpa の設定の「エージェントを追加」に登録用の URL を貼るか、TUNER_AGENT_URL に #pair=… を付けてください",
		"paired": false,
	})
}
