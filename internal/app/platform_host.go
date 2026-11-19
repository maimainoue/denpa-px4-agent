//go:build windows || darwin

package app

import (
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/card"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/host"
)

// Windows と macOS では DriverHost_PX4 のクライアントになる (仕様書 5 章)。
func init() {
	RegisterOpener(host.DevicePrefix, func(env *Env, spec backend.TunerSpec) (backend.Tuner, error) {
		return host.Open(env.Defs, host.NewTransport(env.Cfg.RuntimeDir, env.Cfg.DriverHostPath), env.Log, spec)
	})
	// 内蔵リーダーはカードのパイプ。外付けリーダーの OS の PC/SC (winscard.dll・PCSC.framework) はまだ無い
	RegisterCardProvider(func(env *Env) card.Provider {
		return host.NewCardProvider(host.NewTransport(env.Cfg.RuntimeDir, env.Cfg.DriverHostPath))
	})
}
