//go:build linux && !nasne

package app

import (
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/card"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/pcsc"
)

// Linux では、内蔵リーダー (px4-drv-pcsc) も外付けリーダー (libccid) も pcscd 経由 (仕様書 9.1)。
// pcscd の socket に直接つなぐ pure Go のクライアントを使う。socket は PCSCLITE_CSOCK_NAME。
func init() {
	RegisterCardProvider(func(env *Env) card.Provider { return pcsc.New(env.Cfg.PcscSock) })
}
