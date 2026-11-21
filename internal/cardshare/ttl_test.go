package cardshare

import (
	"net"
	"testing"
	"time"
)

// TestSetSocketTTL は、ソケットに付けた TTL が読めることを確かめます。
func TestSetSocketTTL(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		time.Sleep(2 * time.Second)
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	tc, ok := c.(*net.TCPConn)
	if !ok {
		t.Fatal("TCP の接続ではありません")
	}
	raw, err := tc.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	if err := setSocketTTL(raw, "tcp4", 2); err != nil {
		t.Fatal(err)
	}
	if err := checkSocketTTL(raw, "tcp4", 2); err != nil {
		t.Fatal(err)
	}
	if got, err := getSocketTTL(raw, "tcp4"); err != nil || got != 2 {
		t.Fatalf("TTL が %d になっていません: %v", got, err)
	}
}

// TestTTLListener は、受けた接続に TTL が付くことを確かめます。
func TestTTLListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	wrapped := &ttlListener{Listener: ln, network: "tcp4", ttl: 2}
	got := make(chan int, 1)
	go func() {
		c, err := wrapped.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		tc, ok := c.(*net.TCPConn)
		if !ok {
			return
		}
		raw, err := tc.SyscallConn()
		if err != nil {
			return
		}
		v, err := getSocketTTL(raw, "tcp4")
		if err != nil {
			return
		}
		got <- v
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	select {
	case v := <-got:
		if v != 2 {
			t.Fatalf("受けた接続の TTL が %d です", v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("受けた接続の TTL が読めません")
	}
}

// TestMeasureRTT は、ループバックの往復時間が測れることを確かめます。
func TestMeasureRTT(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		time.Sleep(time.Second)
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	tc, ok := c.(*net.TCPConn)
	if !ok {
		t.Fatal("TCP の接続ではありません")
	}
	raw, err := tc.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	rtt, err := measureRTT(raw)
	if err != nil {
		t.Fatal(err)
	}
	if rtt < 0 || rtt >= time.Second {
		t.Fatalf("往復時間がおかしいです: %s", rtt)
	}
}
