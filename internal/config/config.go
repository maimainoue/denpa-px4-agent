// Package config は環境変数を読む (仕様書 12.1)。読めない値は既定に戻さず、理由を返す。
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
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

	CardURL        string
	CardSharePort  int
	PcscSock       string
	RuntimeDir     string
	DriverHostPath string
	DriverHostIni  string
	FakeTune       string
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
	if c.CardSharePort, err = intVar(getenv, "CARD_SHARE_PORT", 0, 0, 65535); err != nil {
		return nil, err
	}
	if c.CardSharePort != 0 && c.CardSharePort == c.Port {
		return nil, fmt.Errorf("CARD_SHARE_PORT は AGENT_PORT と別の値にします")
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
