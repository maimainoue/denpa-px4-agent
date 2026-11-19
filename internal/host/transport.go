package host

import (
	"errors"
	"io"
	"sync"
)

// Pipe は DriverHost_PX4 の 3 本のパイプ。
type Pipe int

const (
	CtrlPipe Pipe = iota
	DataPipe
	CardPipe
)

// Conn は開いたパイプ。Close は読み待ちを解く。
type Conn interface {
	io.ReadWriteCloser
}

// Transport は OS ごとの通信路 (Windows の名前付きパイプ、macOS の UNIX ドメインソケット)。
type Transport interface {
	Dial(p Pipe) (Conn, error)
	Layout() Layout
	// Describe は記録に出すパイプの名前 (`\\.\pipe\px4_ctrl_pipe`・socket のパス)。
	Describe(p Pipe) string
}

// ErrNotConnected は DriverHost_PX4 に繋がらないとき。
type ErrNotConnected struct{ Pipe string }

func (e *ErrNotConnected) Error() string {
	return "DriverHost_PX4 に繋がりません (" + e.Pipe + ")"
}

// rpc は「送って同じ長さを受ける」往復 (制御とカード)。Windows は 1 メッセージが 1 コマンド、
// macOS のソケットには境目が無いので、送った長さぶんを読み切る。
type rpc struct {
	mu   sync.Mutex
	conn Conn
}

func (r *rpc) call(req []byte) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn == nil {
		return nil, io.ErrClosedPipe
	}
	if _, err := r.conn.Write(req); err != nil {
		return nil, err
	}
	resp := make([]byte, len(req))
	if _, err := io.ReadFull(r.conn, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (r *rpc) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn != nil {
		r.conn.Close()
		r.conn = nil
	}
}

func isBroken(err error) bool {
	return err != nil && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe) || isPipeError(err))
}
