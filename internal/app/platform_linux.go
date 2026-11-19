//go:build linux && !nasne

package app

import (
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/chardev"
)

func init() {
	RegisterOpener(chardev.DevicePrefix, func(env *Env, spec backend.TunerSpec) (backend.Tuner, error) {
		return chardev.Open(env.Defs, "/sys", spec)
	})
	RegisterDetector(func(env *Env) ([]backend.TunerSpec, error) {
		return chardev.Detect(env.Defs, "/dev", "/sys")
	})
}
