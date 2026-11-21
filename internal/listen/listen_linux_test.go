//go:build linux

package listen

import (
	"io"
	"net"
	"testing"
	"time"
)

func echoOnce(t *testing.T, ln net.Listener) {
	t.Helper()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		io.Copy(c, c)
	}()
	c, err := net.DialTimeout("tcp", ln.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("%q %v", buf, err)
	}
}

func TestWrapListener_通常の経路(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := Wrap(raw)
	defer ln.Close()
	echoOnce(t, ln)
	if ln.(*accept4Fallback).fallback.Load() {
		t.Error("accept4 が使えれば代替しない")
	}
}

// accept4 の無いカーネル (nasne の 2.6.29) を想定して、代替の経路を強制的に通す。
func TestWrapListener_accept4が無いときの代替の経路(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := Wrap(raw).(*accept4Fallback)
	if err := ln.startFallback(); err != nil {
		t.Fatal(err)
	}
	echoOnce(t, ln)

	// 通常の net.Conn として使える (TCPConn・アドレス)
	done := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); done <- c }()
	cl, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	srv := <-done
	if _, ok := srv.(*net.TCPConn); !ok || srv.RemoteAddr().String() != cl.LocalAddr().String() {
		t.Errorf("%T %v", srv, srv.RemoteAddr())
	}
	srv.Close()

	// 閉じれば、待っている Accept が戻る
	errc := make(chan error, 1)
	go func() { _, err := ln.Accept(); errc <- err }()
	time.Sleep(200 * time.Millisecond)
	ln.Close()
	select {
	case err := <-errc:
		if err == nil {
			t.Error("閉じたあとの Accept は失敗する")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close しても Accept が戻らない")
	}
}
