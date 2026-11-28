//go:build darwin

package app

import (
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/host"
)

// macOS は、挿さっている機材を IOKit (IORegistry の IOUSBHostDevice) で数える (仕様書 5.3)。
// DriverHost_PX4 は libusb で機材を見るので、ドライバーの有無は区別せず、USB の ID の数だけで並べる。
func init() {
	RegisterDetector(func(env *Env) ([]backend.TunerSpec, error) {
		return host.Detect(env.Defs, host.NewEnumerator(), env.Log)
	})
}
