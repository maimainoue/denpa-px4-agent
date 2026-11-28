//go:build windows

package app

import (
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/host"
)

// Windows は、挿さっている機材を SetupAPI で数える (仕様書 5.3)。macOS の検出 (IOKit) はまだ無い。
func init() {
	RegisterDetector(func(env *Env) ([]backend.TunerSpec, error) {
		return host.Detect(env.Defs, host.NewEnumerator(), env.Log)
	})
}
