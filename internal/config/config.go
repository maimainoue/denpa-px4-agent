// Package config は環境変数を読む (仕様書 12.1)。読めない値は既定に戻さず、理由を返す。
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config は環境変数から決めた設定。
type Config struct {
	Port      int
	Host      string
	PairToken string
	Name      string
	Home      string

	TunersFile   string
	ChannelsFile string
	DevicesFile  string
	CardsFile    string
	TLSDir       string

	MediaDir string
	RawDir   string

	ShutdownWait time.Duration
	Version      string

	CardURL           string
	CardSharePort     int
	CardShareNetworks string
	// CardTTL は鍵を配るときと貰うときの TTL (IPv6 は hop limit)。既定 1、1〜8。
	CardTTL int
	// CardShareMaxRTTMs は配る側が見る往復時間の上限 (ミリ秒)。0 なら見ません。
	CardShareMaxRTTMs int
	PcscSock          string
	RuntimeDir        string
	DriverHostPath    string
	DriverHostIni     string
	FakeTune          string
	// StreamQueueBytes は利用者ごとの溜める上限 (STREAM_QUEUE_BYTES)。0 なら既定の 64MB。
	// メモリの小さい機材 (nasne の上) で下げる (仕様書 6.5)。
	StreamQueueBytes int64
}

// Load は getenv から設定を作る。goos は runtime.GOOS、exe は実行ファイルのパス。
func Load(getenv func(string) string, goos string, exe string) (*Config, error) {
	c := &Config{}
	var err error
	if c.Port, err = intVar(getenv, "AGENT_PORT", 25252, 1, 65535); err != nil {
		return nil, err
	}
	if h := getenv("AGENT_HOST"); h != "" {
		if net.ParseIP(h) == nil {
			return nil, fmt.Errorf("AGENT_HOST は IP アドレスにします (%s)", h)
		}
		c.Host = h
	}
	c.PairToken = getenv("AGENT_PAIR_TOKEN")
	c.Name = getenv("AGENT_NAME")
	if c.Name == "" {
		c.Name, _ = os.Hostname()
	}
	c.Version = def(getenv("DENPA_VERSION"), "dev")

	home := getenv("DENPA_HOME")
	if home == "" {
		if uh, e := os.UserHomeDir(); e == nil {
			home = filepath.Join(uh, "denpa")
		}
	}
	c.Home = home
	cfgDir := filepath.Join(home, "config")
	if goos == "linux" {
		cfgDir = "/config"
	}
	c.TunersFile = def(getenv("TUNERS_FILE"), filepath.Join(cfgDir, "tuners.json"))
	c.ChannelsFile = def(getenv("CHANNELS_FILE"), filepath.Join(cfgDir, "channels.json"))
	c.DevicesFile = def(getenv("DEVICES_FILE"), filepath.Join(cfgDir, "devices.json"))
	c.CardsFile = filepath.Join(filepath.Dir(c.TunersFile), "cards.json")
	c.TLSDir = filepath.Join(filepath.Dir(c.TunersFile), "tls")

	mediaDefault := filepath.Join(home, "recorded")
	rawDefault := mediaDefault
	if goos == "linux" {
		mediaDefault = "/media"
		rawDefault = ""
	}
	c.MediaDir = def(getenv("MEDIA_DIR"), mediaDefault)
	if rawDefault == "" {
		rawDefault = filepath.Join(c.MediaDir, "raw")
	}
	c.RawDir = def(getenv("RAW_DIR"), rawDefault)

	ms, err := intVar(getenv, "SHUTDOWN_WAIT", 21600000, 0, 1<<31-1)
	if err != nil {
		return nil, err
	}
	c.ShutdownWait = time.Duration(ms) * time.Millisecond

	c.CardURL = getenv("CARD_URL")
	if c.CardURL != "" {
		if err := ValidateCardURL(c.CardURL); err != nil {
			return nil, err
		}
	}
	if c.CardSharePort, err = intVar(getenv, "CARD_SHARE_PORT", 0, 0, 65535); err != nil {
		return nil, err
	}
	if c.CardSharePort != 0 && c.CardSharePort == c.Port {
		return nil, fmt.Errorf("CARD_SHARE_PORT は AGENT_PORT と別の値にします")
	}
	c.CardShareNetworks = def(getenv("CARD_SHARE_NETWORKS"), DefaultShareNetworks())
	if c.CardSharePort != 0 {
		if _, err := ParseShareNetworks(c.CardShareNetworks); err != nil {
			return nil, err
		}
	}
	if c.CardTTL, err = intVar(getenv, "CARD_TTL", 1, 1, 8); err != nil {
		return nil, err
	}
	if c.CardShareMaxRTTMs, err = intVar(getenv, "CARD_SHARE_MAX_RTT", 0, 0, 1<<31-1); err != nil {
		return nil, err
	}
	c.PcscSock = def(getenv("PCSCLITE_CSOCK_NAME"), "/run/pcscd/pcscd.comm")
	c.RuntimeDir = def(getenv("PX4_RUNTIME_DIR"), "/tmp")
	hostName := "DriverHost_PX4"
	if goos == "windows" {
		hostName += ".exe"
	}
	c.DriverHostPath = def(getenv("DRIVER_HOST_PATH"), filepath.Join(filepath.Dir(exe), hostName))
	c.DriverHostIni = def(getenv("DRIVER_HOST_INI"), filepath.Join(filepath.Dir(c.DriverHostPath), "DriverHost_PX4.ini"))
	c.FakeTune = getenv("FAKE_TUNE")
	q, err := intVar(getenv, "STREAM_QUEUE_BYTES", 0, 0, 1<<31-1)
	if err != nil {
		return nil, err
	}
	if q != 0 && q < 1<<20 {
		return nil, fmt.Errorf("STREAM_QUEUE_BYTES は 1048576 (1MiB) 以上にします (%d)", q)
	}
	c.StreamQueueBytes = int64(q)
	return c, nil
}

func def(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func intVar(getenv func(string) string, name string, d, lo, hi int) (int, error) {
	s := getenv(name)
	if s == "" {
		return d, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("%s は %d〜%d の整数にします (%s)", name, lo, hi, s)
	}
	return n, nil
}

// defaultShareNetworks は鍵を配る相手の既定の範囲です。仕様書 9.5。
// CGNAT (100.64.0.0/10) は入れません。Tailscale などが拠点の外を同じ網に見せるためです。
var defaultShareNetworks = []string{
	"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
	"::1/128", "fc00::/7", "fe80::/10",
}

// ValidateCardURL は鍵を貰う URL が http://ホスト:ポート か確かめます。
// 鍵を配るポートは平文の HTTP なので、https は受けません。
func ValidateCardURL(u string) error {
	parsed, err := url.Parse(u)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" {
		return fmt.Errorf("CARD_URL は http://ホスト:ポート にします (%q)", u)
	}
	return nil
}

// ParseShareNetworks は CARD_SHARE_NETWORKS の文字列を読み、既定の範囲の中に
// 収まっているか確かめます。範囲かアドレス 1 つをカンマで区切ります。
func ParseShareNetworks(s string) ([]*net.IPNet, error) {
	defaults := mustParseNetworks(defaultShareNetworks)
	var out []*net.IPNet
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("CARD_SHARE_NETWORKS を読めません (空の範囲があります)")
		}
		var n *net.IPNet
		if strings.Contains(part, "/") {
			_, parsed, err := net.ParseCIDR(part)
			if err != nil {
				return nil, fmt.Errorf("CARD_SHARE_NETWORKS を読めません (%q)", part)
			}
			n = parsed
		} else {
			ip := net.ParseIP(part)
			if ip == nil {
				return nil, fmt.Errorf("CARD_SHARE_NETWORKS を読めません (%q)", part)
			}
			bits := 128
			if ip4 := ip.To4(); ip4 != nil {
				ip = ip4
				bits = 32
			}
			n = &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}
		}
		if !withinAny(n, defaults) {
			return nil, fmt.Errorf("CARD_SHARE_NETWORKS に %s は書けません (LAN の中だけです)", part)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("CARD_SHARE_NETWORKS を読めません (範囲がありません)")
	}
	return out, nil
}

func mustParseNetworks(list []string) []*net.IPNet {
	var out []*net.IPNet
	for _, s := range list {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			panic(err)
		}
		out = append(out, n)
	}
	return out
}

// withinAny は範囲 n が既定の範囲のどれか 1 つにすっぽり収まっているかです。
func withinAny(n *net.IPNet, defaults []*net.IPNet) bool {
	for _, d := range defaults {
		if containsNet(d, n) {
			return true
		}
	}
	return false
}

// containsNet は外側の範囲 outer が内側の範囲 inner をすっぽり含んでいるかです。
// IPv4 射影アドレス (::ffff:192.168.1.2) は IPv4 として見ます。
func containsNet(outer, inner *net.IPNet) bool {
	outerIP := outer.IP
	innerIP := inner.IP
	if outerIP.To4() != nil && innerIP.To4() != nil {
		outerIP = outerIP.To4()
		innerIP = innerIP.To4()
	} else if (outerIP.To4() == nil) != (innerIP.To4() == nil) {
		return false
	}
	outerOnes, outerBits := outer.Mask.Size()
	innerOnes, innerBits := inner.Mask.Size()
	if outerBits != innerBits || innerOnes < outerOnes {
		return false
	}
	return outer.Contains(innerIP) && outer.Contains(lastIP(inner))
}

// lastIP は範囲のいちばん後ろのアドレスです。
func lastIP(n *net.IPNet) net.IP {
	ip := n.IP
	mask := n.Mask
	out := make(net.IP, len(ip))
	for i := range ip {
		out[i] = ip[i] | ^mask[i]
	}
	return out
}

// DefaultShareNetworks は既定の範囲の文字列です。画面に出すときに使います。
func DefaultShareNetworks() string {
	return strings.Join(defaultShareNetworks, ",")
}
