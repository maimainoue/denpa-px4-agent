//go:build linux

// Package listen は、accept4 が無いカーネルで accept に切り替える待ち受け口です。
// メインの待ち受け (App.Listen) と鍵を配る待ち受けの両方で使います。
package listen

import (
	"errors"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// accept4Fallback は、accept4 が無いカーネルで accept に切り替える待ち受け口。
//
// Go の標準ライブラリは accept4 しか使わない。MIPS では accept4 が Linux 2.6.32 で入ったので、
// nasne の公式カーネル (2.6.29) では `accept4: function not implemented` で待ち受けられない (実機)。
// 最初の Accept が ENOSYS で失敗したら、待ち受けの fd を Go の netpoll (epoll) で待ち、
// 読めるようになったときだけ、非ブロックの accept(2) を直に呼ぶ。
//
// blocking の accept(2) を別のスレッドで待たない理由: 信号 (SIGCHLD・SIGPROF) に中断された accept が、
// 古い MIPS のカーネルで ENOSYS を返して、待ち受けが止まった (実機で、プロファイルを取っていたときに落ちた)。
// 子プロセス (nasne-recpt1) が終わるたびに SIGCHLD が飛ぶので、本番でも起きうる。
type accept4Fallback struct {
	net.Listener

	fallback atomic.Bool
	file     *os.File        // 待ち受けの fd の複製 (非ブロック)。Go の netpoll に待たせる
	raw      syscall.RawConn // file の RawConn。TCPListener の RawConn は Read を使えない (EINVAL)
}

func Wrap(ln net.Listener) net.Listener { return &accept4Fallback{Listener: ln} }

func (l *accept4Fallback) Accept() (net.Conn, error) {
	if !l.fallback.Load() {
		c, err := l.Listener.Accept()
		if err == nil || !errors.Is(err, syscall.ENOSYS) {
			return c, err
		}
		if err := l.startFallback(); err != nil {
			return nil, err
		}
	}
	return l.acceptSyscall()
}

func (l *accept4Fallback) startFallback() error {
	if l.fallback.Load() {
		return nil
	}
	tl, ok := l.Listener.(*net.TCPListener)
	if !ok {
		return errors.New("accept4 を使えない待ち受け口は、TCP だけ代替できます")
	}
	lraw, err := tl.SyscallConn()
	if err != nil {
		return err
	}
	var dup int
	var derr error
	if err := lraw.Control(func(fd uintptr) { dup, derr = syscall.Dup(int(fd)) }); err != nil {
		return err
	}
	if derr != nil {
		return derr
	}
	syscall.CloseOnExec(dup)
	// 同じ接続の待ち受けを別の fd で持つ。非ブロックにして os.File にすると、Go の netpoll に登録される
	if err := syscall.SetNonblock(dup, true); err != nil {
		syscall.Close(dup)
		return err
	}
	f := os.NewFile(uintptr(dup), "listener")
	raw, err := f.SyscallConn()
	if err != nil {
		f.Close()
		return err
	}
	l.file, l.raw = f, raw
	l.fallback.Store(true)
	return nil
}

func (l *accept4Fallback) acceptSyscall() (net.Conn, error) {
	for {
		var nfd int
		var aerr error
		err := l.raw.Read(func(fd uintptr) bool {
			nfd, aerr = rawAccept(int(fd))
			// 読めるまで待ち直す。中断・相手が先に切ったときは、そのまま次を待つ
			return aerr != syscall.EAGAIN && aerr != syscall.EINTR && aerr != syscall.ECONNABORTED
		})
		if err != nil {
			return nil, err // 閉じたあとは net.ErrClosed などが返る
		}
		if aerr != nil {
			return nil, &net.OpError{Op: "accept", Net: "tcp", Err: aerr}
		}
		syscall.CloseOnExec(nfd)
		f := os.NewFile(uintptr(nfd), "accepted")
		c, err := net.FileConn(f) // fd を複製して、非ブロックの通常の net.Conn にする
		f.Close()
		if err != nil {
			return nil, err
		}
		return c, nil
	}
}

// rawAccept は accept(2) を直に呼ぶ。syscall.Accept は中で accept4 を呼ぶので、accept4 の無いカーネルでは使えない。
// 待ち受けの fd は非ブロックなので、待たずに戻る。
func rawAccept(fd int) (int, error) {
	var rsa syscall.RawSockaddrAny
	alen := uint32(syscall.SizeofSockaddrAny)
	r, _, errno := syscall.RawSyscall(syscall.SYS_ACCEPT, uintptr(fd), uintptr(unsafe.Pointer(&rsa)), uintptr(unsafe.Pointer(&alen)))
	if errno != 0 {
		return -1, errno
	}
	return int(r), nil
}

func (l *accept4Fallback) Close() error {
	err := l.Listener.Close()
	if l.file != nil {
		l.file.Close() // 待っている Accept が戻る
	}
	return err
}
