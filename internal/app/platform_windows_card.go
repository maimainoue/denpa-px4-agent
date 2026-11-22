//go:build windows

package app

import (
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/card"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/winscard"
)

// Windows の外付けリーダーは、OS の PC/SC (System32 の winscard.dll) で使う (仕様書 9.1)。
// WINSCARD_DLLS に絶対パスを書いた DLL があれば、System32 のあとに足す。
// 内蔵リーダーは platform_host.go のカードのパイプ。リーダーの並びは card.Manager が内蔵を先にする。
func init() {
	RegisterCardProvider(func(env *Env) card.Provider {
		paths := append([]string{winscard.DefaultDLL()}, env.Cfg.WinscardDLLs...)
		return winscard.New(paths, winscard.LoadDLL, env.Log)
	})
}
