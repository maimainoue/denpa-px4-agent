package app

import (
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/card"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/cardshare"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/config"
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
