// Package app は設定・記録・TLS・取り合い・API・バックエンドを繋いで、サーバとして動かす。
package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/api"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend/cmdbackend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/card"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/cardshare"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/config"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/devices"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/logx"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/manager"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/store"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/tlsx"
)

// Env はバックエンドが開くときに使うもの。
type Env struct {
	Cfg  *config.Config
	Defs *devices.Defs
	Log  backend.Logf
}

// Opener は device の接頭辞 (`px4:`・`px4host:`) ごとのバックエンド。OS ごとのファイルが登録する。
type Opener func(env *Env, spec backend.TunerSpec) (backend.Tuner, error)

// Detector は挿さっている機材を並べる。OS ごとのファイルが登録する。
type Detector func(env *Env) ([]backend.TunerSpec, error)

// CardProvider はカードへの経路 (pcscd・DriverHost_PX4 のカードのパイプ・OS の PC/SC)。内蔵を先に並べる。
type CardProvider func(env *Env) card.Provider

var (
	openers       = map[string]Opener{}
	detectors     []Detector
	cardProviders []CardProvider
)

// RegisterOpener は接頭辞のバックエンドを登録する (init から呼ぶ)。
func RegisterOpener(prefix string, o Opener) { openers[prefix] = o }

// RegisterDetector は検出を登録する (init から呼ぶ)。
func RegisterDetector(d Detector) { detectors = append(detectors, d) }

// RegisterCardProvider はカードへの経路を登録する (init から呼ぶ)。
func RegisterCardProvider(p CardProvider) { cardProviders = append(cardProviders, p) }

// App はサーバ 1 つぶん。
type App struct {
	Cfg      *config.Config
	Log      *logx.Logger
	ID       *tlsx.Identity
	Mgr      *manager.Manager
	Hub      *api.Hub
	Channels *store.Channels
	Tuners   store.Tuners
	Cards    *store.Cards
	Defs     *devices.Defs
	Card     *card.Manager

	env   *Env
	cmd   *cmdbackend.Backend
	share *cardshare.Server // CARD_SHARE_PORT を書いたときだけ開く鍵を配るサーバ

	mu           sync.Mutex
	defined      bool
	lastDetected []backend.TunerSpec // 前回の検出の結果 (a.mu で守る)
}

// New は設定から App を作る。
func New(cfg *config.Config, log *logx.Logger) (*App, error) {
	a := &App{Cfg: cfg, Log: log, Hub: api.NewHub()}
	a.Channels = &store.Channels{Path: cfg.ChannelsFile}
	a.Tuners = store.Tuners{Path: cfg.TunersFile}
	a.Cards = &store.Cards{Path: cfg.CardsFile}

	a.Defs = devices.Builtin()
	a.Defs.Overlay(devices.LoadFile(cfg.DevicesFile, log.Printf), log.Printf)
	a.env = &Env{Cfg: cfg, Defs: a.Defs, Log: log.Printf}
	a.cmd = cmdbackend.New(cmdbackend.Config{Defs: a.Defs, FakeTune: cfg.FakeTune, Log: log.Printf})

	id, err := tlsx.Load(cfg.TLSDir, cfg.PairToken, log.Printf)
	if err != nil {
		return nil, fmt.Errorf("鍵と証明書を用意できません: %w", err)
	}
	a.ID = id

	var providers []card.Provider
	for _, p := range cardProviders {
		providers = append(providers, p(a.env))
	}
	a.Card = card.New(providers, a.Cards, log.Printf, card.Hooks{
		Active: func() []string { return a.Mgr.ActiveCards() },
		Device: func() map[string][]string { return a.Mgr.DecryptedByDeviceTuners() },
		Notify: func() { a.Hub.Notify("card") },
	})
	mcfg := manager.Config{
		Log:             log.Printf,
		Open:            a.openBackend,
		Notify:          a.Hub.Notify,
		SinkLimit:       cfg.StreamQueueBytes,
		NewProcessor:    func(name string) manager.Processor { return a.Card.NewStream(name) },
		NewTLVProcessor: func(name string) manager.Processor { return a.Card.NewTLVStream(name) },
	}
	a.Mgr = manager.New(mcfg)
	return a, nil
}

func (a *App) openBackend(spec backend.TunerSpec) (backend.Tuner, error) {
	d := spec.Device
	if d == "" || strings.HasPrefix(d, "cmd:") {
		if d == "" && a.Cfg.FakeTune == "" {
			return nil, fmt.Errorf("device がありません")
		}
		return a.cmd.Open(spec)
	}
	for prefix, o := range openers {
		if strings.HasPrefix(d, prefix) {
			return o(a.env, spec)
		}
	}
	return nil, fmt.Errorf("このエージェントでは %s を開けません (対応していない形か、この OS では使えません)", d)
}

// Start は起動のときの定義を読み、検出を始める。
// Start はカードと鍵を配るサーバを起動します。CARD_SHARE_PORT を書いたときだけ、
// 鍵を配るサーバも開きます。開けなければ起動そのものが失敗します
// (書いたのに配らない、という黙った失敗にしないためです)。
func (a *App) Start(ctx context.Context) error {
	specs, found := a.Tuners.Load(a.Log.Printf)
	a.mu.Lock()
	a.defined = found
	a.mu.Unlock()
	if found {
		a.Log.Printf("チューナーの定義を読みました (%d 台)", len(specs))
		a.Mgr.SetSpecs(specs, false)
	} else {
		a.Log.Printf("チューナーの定義がありません。挿さっている機材を探します")
	}
	if a.Cfg.CardURL != "" {
		a.Card.SetRemote(cardshare.NewRemoteCard(a.Cfg.CardURL, a.Cfg.CardTTL, a.Log.Printf))
		a.Log.Printf("鍵を貰います (%s)", a.Cfg.CardURL)
	}
	a.Card.Start(ctx)
	if a.Cfg.CardSharePort != 0 {
		nets, err := config.ParseShareNetworks(a.Cfg.CardShareNetworks)
		if err != nil {
			return fmt.Errorf("CARD_SHARE_NETWORKS が読めません (%v)", err)
		}
		// 配る側は手元のカードだけを配ります。CARD_URL を書いていても、貰った鍵をさらに配ることはしません。
		a.share = cardshare.New(a.Cfg.CardSharePort, nets, a.Card.LocalCard(), wrapListener, a.Cfg.CardTTL, a.Cfg.CardShareMaxRTTMs, a.Log.Printf)
		if err := a.share.Start(); err != nil {
			return err
		}
	}
	a.detect()
	go func() {
		tk := time.NewTicker(5 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				a.detect()
			}
		}
	}()
	return nil
}

// Redetect は定義を消したあとに自動検出へ戻す。
func (a *App) Redetect() {
	a.mu.Lock()
	a.defined = false
	// 前回の検出と同じ結果でも入れ直す。ここで忘れないと、一覧を空にしたまま戻らない
	// (画面の「自動検出」で、チューナーが消えたまま戻らなかった)
	a.lastDetected = nil
	a.mu.Unlock()
	a.Mgr.SetSpecs(nil, true)
	a.detect()
}

func (a *App) detect() {
	var found []backend.TunerSpec
	for _, d := range detectors {
		specs, err := d(a.env)
		if err != nil {
			a.Log.Printf("機材を探せません: %v", err)
			continue
		}
		found = append(found, specs...)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.defined {
		// 定義があれば、前に無かった機材を末尾に足す (4.2)
		cur := a.Mgr.Specs()
		have := map[string]bool{}
		names := map[string]bool{}
		for _, s := range cur {
			have[s.Device] = true
			names[s.Name] = true
		}
		added := false
		for _, s := range found {
			if have[s.Device] {
				continue
			}
			for n := 2; names[s.Name]; n++ {
				s.Name = fmt.Sprintf("%s (%d)", s.Name, n)
			}
			names[s.Name], have[s.Device] = true, true
			cur = append(cur, s)
			added = true
		}
		if added {
			a.Mgr.SetSpecs(cur, false)
		}
		return
	}
	if reflect.DeepEqual(found, a.lastDetected) {
		return
	}
	a.lastDetected = found
	for _, s := range found {
		a.Log.Printf("機材を見つけました: %s (%s)", s.Name, s.Device)
	}
	a.Mgr.SetSpecs(found, true)
}

// Handler は HTTP の経路。
func (a *App) Handler() http.Handler {
	return api.New(api.Deps{
		Cfg: a.Cfg, Log: a.Log, ID: a.ID, Mgr: a.Mgr, Channels: a.Channels, Tuners: a.Tuners,
		Hub: a.Hub, Card: a.Card, Redetect: a.Redetect,
		ValidateDevice: func(device string) error {
			if strings.HasPrefix(device, "cmd:") {
				return a.cmd.Validate(device)
			}
			return nil
		},
	}).Handler()
}

// Serve は HTTPS で待ち受ける。ctx が終わったら止める。
func (a *App) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:   a.Handler(),
		TLSConfig: a.ID.Config(),
		// 長く流す接続なので WriteTimeout は付けない
		ReadHeaderTimeout: 30 * time.Second,
		ErrorLog:          nil,
		TLSNextProto:      map[string]func(*http.Server, *tls.Conn, http.Handler){},
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ServeTLS(ln, "", "") }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	a.shutdown(srv)
	return nil
}

// shutdown は止まり方 (13)。rec の利用者がいる間は SHUTDOWN_WAIT まで待つ。
func (a *App) shutdown(srv *http.Server) {
	if a.Cfg.ShutdownWait > 0 && a.Mgr.Recording() {
		a.Log.Printf("録画が終わるまで待ちます (最長 %v)", a.Cfg.ShutdownWait)
		deadline := time.Now().Add(a.Cfg.ShutdownWait)
		for a.Mgr.Recording() && time.Now().Before(deadline) {
			time.Sleep(time.Second)
		}
	}
	a.Hub.CloseAll()
	a.Mgr.Shutdown()
	if a.share != nil {
		_ = a.share.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	srv.Close()
}

// Listen は AGENT_HOST・AGENT_PORT で待つ。
func (a *App) Listen() (net.Listener, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(a.Cfg.Host, strconv.Itoa(a.Cfg.Port)))
	if err != nil {
		return nil, err
	}
	return wrapListener(ln), nil // accept4 の無い古いカーネル (nasne の 2.6.29) では accept に切り替える
}

// PairURL は組むときの URL。
func (a *App) PairURL() string {
	host := a.Cfg.Name
	if host == "" {
		host, _ = os.Hostname()
	}
	return fmt.Sprintf("https://%s:%d#pair=%s", host, a.Cfg.Port, a.ID.Token())
}

// Listen0 は空いているポートで待つ (テスト用)。
func (a *App) Listen0() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }
