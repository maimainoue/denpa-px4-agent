package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/config"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/tlsx"
)

// status は動いているエージェントの /denpa/tuners を、自分の証明書 (tls/agent.key) で繋いで標準出力に出す。
// 自分の証明書は組まなくても通る (agent.key を読めるのはその機械の管理者だけ)。
// 繋がらない・200 でないときは、理由を標準エラーに出して 1 を返す (標準出力には何も出さない)。
func status(cfg *config.Config) int {
	keyPath, crtPath := filepath.Join(cfg.TLSDir, "agent.key"), filepath.Join(cfg.TLSDir, "agent.crt")
	cert, err := tls.LoadX509KeyPair(crtPath, keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s に agent.key / agent.crt がありません (エージェントを一度起動すると作られます): %v\n", cfg.TLSDir, err)
		return 1
	}
	expected := tlsx.Fingerprint(cert.Certificate[0])

	// AGENT_HOST が特定のアドレスならそこへ。全部のインターフェースで待っているなら 127.0.0.1
	host := "127.0.0.1"
	if ip := net.ParseIP(cfg.Host); ip != nil && !ip.IsUnspecified() {
		host = cfg.Host
	}
	url := "https://" + net.JoinHostPort(host, strconv.Itoa(cfg.Port)) + "/denpa/tuners"

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			// 相手は自己署名なので、CA では見ずに、自分の指紋と同じ証明書かどうかだけを見る
			InsecureSkipVerify: true,
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				if len(raw) == 0 || tlsx.Fingerprint(raw[0]) != expected {
					return fmt.Errorf("相手の証明書が、このエージェントのものではありません")
				}
				return nil
			},
		}},
	}
	res, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", url, err)
		return 1
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", url, err)
		return 1
	}
	if res.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "%s: %d %s\n", url, res.StatusCode, body)
		return 1
	}
	fmt.Println(string(body))
	return 0
}
