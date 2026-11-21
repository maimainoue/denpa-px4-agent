//go:build fakecard

package app

import (
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/card"
)

// 試験カードは -tags fakecard で組んだときだけ登録します。FAKE_CARD=1 のときだけ使います (仕様書 12.1)。
func init() {
	RegisterCardProvider(func(env *Env) card.Provider { return card.FakeProvider() })
}
