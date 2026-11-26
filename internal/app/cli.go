package app

import (
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend/cmdbackend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/card"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/cardshare"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/config"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/devices"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/logx"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/store"
)

// NewCardManager は、サーバを立てずにカードだけを使う CLI (`--decode-file` など) のカード管理 (仕様書 15)。
// tls/ の鍵と証明書には触らない。CARD_URL があれば、手元のカードの代わりに鍵を貰う。
// カードは使うとき (ECM が来たとき) に開く。
func NewCardManager(cfg *config.Config, log *logx.Logger) *card.Manager {
	env := &Env{Cfg: cfg, Log: log.Printf}
	var providers []card.Provider
	for _, p := range cardProviders {
		providers = append(providers, p(env))
	}
	m := card.New(providers, &store.Cards{Path: cfg.CardsFile}, log.Printf, card.Hooks{})
	if cfg.CardURL != "" {
		m.SetRemote(cardshare.NewRemoteCard(cfg.CardURL, cfg.CardTTL, log.Printf))
	}
	return m
}

// DevicesReport は `--devices` の中身 (仕様書 15)。
type DevicesReport struct {
	Defs []devices.Def // 機材の定義 (組み込み + devices.json)。定義の順

	// 自動検出。Linux だけ (px4_drv の chardev)。Windows と macOS には検出が無い (HasDetector が偽)
	HasDetector bool
	Detected    []backend.TunerSpec
	DetectErrs  []string

	// tuners.json の定義 (found が偽なら、ファイルが無い)
	TunersFile string
	Configured []backend.TunerSpec
	Models     []devices.Resolved // Configured と同じ並び。device から引いた機種 (Defs.ModelOf)
	Found      bool
}

// LoadDefs は組み込みの機種表に devices.json を重ねる (サーバの起動と同じ)。
func LoadDefs(cfg *config.Config, log *logx.Logger) *devices.Defs {
	defs := devices.Builtin()
	defs.Overlay(devices.LoadFile(cfg.DevicesFile, log.Printf), log.Printf)
	return defs
}

// ListDevices は機材の定義と、いま見つかっている機材と、tuners.json の定義を集める (`--devices`)。
// 機材は開かない (検出は、デバイスファイルや USB の一覧を見るだけ)。
func ListDevices(cfg *config.Config, log *logx.Logger) DevicesReport {
	defs := LoadDefs(cfg, log)
	rep := DevicesReport{Defs: defs.All(), HasDetector: len(detectors) > 0, TunersFile: cfg.TunersFile}
	env := &Env{Cfg: cfg, Defs: defs, Log: log.Printf}
	for _, d := range detectors {
		specs, err := d(env)
		if err != nil {
			rep.DetectErrs = append(rep.DetectErrs, err.Error())
			continue
		}
		rep.Detected = append(rep.Detected, specs...)
	}
	rep.Configured, rep.Found = store.Tuners{Path: cfg.TunersFile}.Load(log.Printf)
	for _, s := range rep.Configured {
		rep.Models = append(rep.Models, defs.ModelOf(s.Device))
	}
	return rep
}

// OpenTuner は、サーバを立てずに機材を 1 台開く (`--tune`)。device の解釈と開き方は、サーバと同じ。
// spec.Name は記録に出る名前、spec.Lnb は衛星のときの給電 ("15v" か "11v"、空なら無し)。
// 閉じるのは呼ぶ側 (Stop と Close)。
func OpenTuner(cfg *config.Config, log *logx.Logger, spec backend.TunerSpec) (backend.Tuner, error) {
	defs := LoadDefs(cfg, log)
	env := &Env{Cfg: cfg, Defs: defs, Log: log.Printf}
	cmd := cmdbackend.New(cmdbackend.Config{Defs: defs, FakeTune: cfg.FakeTune, Log: log.Printf})
	return openBackend(env, cmd, spec)
}
