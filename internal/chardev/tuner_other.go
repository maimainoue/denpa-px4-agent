//go:build !linux

package chardev

import (
	"errors"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/devices"
)

// Open は Linux 以外では使えない。
func Open(defs *devices.Defs, sysDir string, spec backend.TunerSpec) (backend.Tuner, error) {
	return nil, errors.New("px4: は Linux だけで使えます (Windows と macOS は px4host:)")
}
