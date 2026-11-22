//go:build darwin

package app

import (
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/card"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/winscard"
)

// macOS の外付けリーダーは、OS の PC/SC (PCSC.framework) で使う (仕様書 9.1)。
// cgo を使わず、purego で dlopen する。PLEX の内蔵リーダーは PCSC.framework には出ない。
// 内蔵リーダーは platform_host.go のカードのパイプ。リーダーの並びは card.Manager が内蔵を先にする。
func init() {
	RegisterCardProvider(func(env *Env) card.Provider {
		return winscard.New([]string{winscard.DefaultDLL()}, winscard.LoadDLL, env.Log)
	})
}
