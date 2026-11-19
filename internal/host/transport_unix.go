//go:build !windows

package host

import (
	"errors"
	"net"
	"path/filepath"
	"syscall"
)

// macOS では UNIX ドメインソケット (`$PX4_RUNTIME_DIR/px4_*_pipe.sock`)。DriverHost_PX4 は root で先に動かしておく。
type unixTransport struct{ dir string }

// NewTransport は通信路を作る。hostPath は Windows だけが使う (DriverHost_PX4 の起動)。
func NewTransport(runtimeDir, hostPath string) Transport { return &unixTransport{dir: runtimeDir} }

func (t *unixTransport) Layout() Layout { return LayoutMac }

func (t *unixTransport) Describe(p Pipe) string {
	return filepath.Join(t.dir, map[Pipe]string{CtrlPipe: "px4_ctrl_pipe.sock", DataPipe: "px4_data_pipe.sock", CardPipe: "px4_card_pipe.sock"}[p])
}

func (t *unixTransport) Dial(p Pipe) (Conn, error) {
	path := t.Describe(p)
	c, err := net.Dial("unix", path)
	if err != nil {
		return nil, &ErrNotConnected{Pipe: path}
	}
	return c, nil
}

func isPipeError(err error) bool {
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET)
}
