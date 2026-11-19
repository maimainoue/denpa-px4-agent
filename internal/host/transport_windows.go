//go:build windows

package host

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows では名前付きパイプ。制御とカードはメッセージ型、TS はバイト型で読む (5.1)。
type winTransport struct {
	hostPath string
	prefix   string // パイプの名前の前半 (既定は `\\.\pipe\`。試験では本物と重ならない名前にする)
	once     sync.Once
	startErr error
}

// NewTransport は通信路を作る。DriverHost_PX4 が動いていなければ、最初に繋ぐときに起動する。
func NewTransport(runtimeDir, hostPath string) Transport {
	return &winTransport{hostPath: hostPath, prefix: `\\.\pipe\`}
}

func (t *winTransport) Layout() Layout { return LayoutWindows }

func (t *winTransport) Describe(p Pipe) string {
	return map[Pipe]string{CtrlPipe: `\\.\pipe\px4_ctrl_pipe`, DataPipe: `\\.\pipe\px4_data_pipe`, CardPipe: `\\.\pipe\px4_card_pipe`}[p]
}

var waitNamedPipe = windows.NewLazySystemDLL("kernel32.dll").NewProc("WaitNamedPipeW")

// ensureHost は BonDriver と同じ手順で DriverHost_PX4 を起動する。
// イベント Global\DriverHost_PX4_StartupEvent を作り、すでにあれば動いている。
func (t *winTransport) ensureHost() error {
	t.once.Do(func() {
		name, _ := windows.UTF16PtrFromString(`Global\DriverHost_PX4_StartupEvent`)
		ev, err := windows.CreateEvent(nil, 1, 0, name)
		if ev == 0 {
			t.startErr = fmt.Errorf("DriverHost_PX4 の起動イベントを作れません: %v", err)
			return
		}
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			windows.CloseHandle(ev)
			return
		}
		cmd := exec.Command(t.hostPath)
		if err := cmd.Start(); err != nil {
			windows.CloseHandle(ev)
			t.startErr = fmt.Errorf("DriverHost_PX4 を起動できません (%s): %v", t.hostPath, err)
			return
		}
		go cmd.Wait() // 自分が起動した DriverHost_PX4 は止めない (ほかのアプリも使う)
		if r, _ := windows.WaitForSingleObject(ev, 10000); r != windows.WAIT_OBJECT_0 {
			t.startErr = errors.New("DriverHost_PX4 の起動を 10 秒待ちましたが、立ち上がりませんでした")
		}
	})
	return t.startErr
}

func (t *winTransport) Dial(p Pipe) (Conn, error) {
	name := t.Describe(p)
	if err := t.ensureHost(); err != nil {
		return nil, fmt.Errorf("%w (%s)", err, name)
	}
	pname, _ := windows.UTF16PtrFromString(name)
	var h windows.Handle
	var err error
	for i := 0; i < 20; i++ {
		waitNamedPipe.Call(uintptr(unsafe.Pointer(pname)), 3000)
		h, err = windows.CreateFile(pname, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) {
			return nil, &ErrNotConnected{Pipe: name}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return nil, &ErrNotConnected{Pipe: name}
	}
	mode := uint32(windows.PIPE_READMODE_MESSAGE)
	if p == DataPipe {
		mode = windows.PIPE_READMODE_BYTE
	}
	if err := windows.SetNamedPipeHandleState(h, &mode, nil, nil); err != nil {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("パイプを設定できません (%s): %v", name, err)
	}
	return &pipeConn{h: h}, nil
}

type pipeConn struct {
	mu     sync.Mutex
	h      windows.Handle
	closed bool
}

// overlapped は 1 回の読み書きを待つ。FILE_FLAG_OVERLAPPED で開いているので、
// TS のパイプの読み待ちの最中にも、同じハンドルに PURGE を書ける (同期 I/O だと書きが塞がる)。
func (c *pipeConn) overlapped(op func(h windows.Handle, b []byte, n *uint32, ov *windows.Overlapped) error, b []byte) (int, error) {
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(ev)
	ov := windows.Overlapped{HEvent: ev}
	var n uint32
	err = op(c.h, b, &n, &ov)
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		err = windows.GetOverlappedResult(c.h, &ov, &n, true)
	}
	return int(n), err
}

func (c *pipeConn) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	n, err := c.overlapped(windows.ReadFile, b)
	if err != nil && !errors.Is(err, windows.ERROR_MORE_DATA) {
		if n > 0 {
			return n, nil
		}
		if errors.Is(err, windows.ERROR_BROKEN_PIPE) || errors.Is(err, windows.ERROR_PIPE_NOT_CONNECTED) {
			return 0, io.EOF
		}
		if errors.Is(err, windows.ERROR_OPERATION_ABORTED) {
			return 0, io.ErrClosedPipe
		}
		return 0, err
	}
	return n, nil
}

func (c *pipeConn) Write(b []byte) (int, error) {
	n, err := c.overlapped(windows.WriteFile, b)
	if errors.Is(err, windows.ERROR_OPERATION_ABORTED) {
		err = io.ErrClosedPipe
	}
	return n, err
}

// Close は読み待ちを解いてから閉じる。
func (c *pipeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	windows.CancelIoEx(c.h, nil)
	return windows.CloseHandle(c.h)
}

func isPipeError(err error) bool {
	return errors.Is(err, windows.ERROR_BROKEN_PIPE) || errors.Is(err, windows.ERROR_NO_DATA) ||
		errors.Is(err, windows.ERROR_PIPE_NOT_CONNECTED) || errors.Is(err, syscall.EPIPE)
}
