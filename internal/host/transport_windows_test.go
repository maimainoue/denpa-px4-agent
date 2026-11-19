//go:build windows

package host

import (
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// 実際の名前付きパイプ (メッセージ型) で、往復とバイト型の読み待ちの解除を確かめる。
// 本物の DriverHost_PX4 が動いていると名前が重なるので、そのときは飛ばす。

// testTransport は本物の DriverHost_PX4 のパイプ名と重ならない名前を使う (動いている本物に繋がないため)。
func testTransport() *winTransport {
	return &winTransport{prefix: fmt.Sprintf(`\\.\pipe\denpa_px4_test_%d_`, os.Getpid())}
}

func serverPipe(t *testing.T, name string, mode uint32) windows.Handle {
	t.Helper()
	n, _ := windows.UTF16PtrFromString(name)
	h, err := windows.CreateNamedPipe(n, windows.PIPE_ACCESS_DUPLEX, mode|windows.PIPE_WAIT, 1, 65536, 65536, 0, nil)
	if err != nil {
		t.Skipf("パイプ %s を作れません (本物の DriverHost_PX4 が動いている?): %v", name, err)
	}
	t.Cleanup(func() { windows.CloseHandle(h) })
	return h
}

func TestWindowsPipe_メッセージ型の往復(t *testing.T) {
	tr := testTransport()
	tr.once.Do(func() {}) // DriverHost_PX4 は起動しない
	srv := serverPipe(t, tr.Describe(CtrlPipe), windows.PIPE_TYPE_MESSAGE|windows.PIPE_READMODE_MESSAGE)
	go func() {
		if err := windows.ConnectNamedPipe(srv, nil); err != nil && err != windows.ERROR_PIPE_CONNECTED {
			return
		}
		buf := make([]byte, 64)
		for {
			var n uint32
			if err := windows.ReadFile(srv, buf, &n, nil); err != nil {
				return
			}
			resp := append([]byte(nil), buf[:n]...)
			resp[4] = statusOK
			var w uint32
			windows.WriteFile(srv, resp, &w, nil)
		}
	}()
	c, err := tr.Dial(CtrlPipe)
	if err != nil {
		t.Fatal(err)
	}
	r := &rpc{conn: c}
	resp, err := r.call(versionReq())
	if err != nil || len(resp) != 16 || respStatus(resp) != statusOK {
		t.Fatalf("%v %v", resp, err)
	}
	resp, err = r.call(captureReq(true))
	if err != nil || len(resp) != 12 || resp[8] != 1 {
		t.Fatalf("2 回目: %v %v", resp, err)
	}
	r.close()
}

func TestWindowsPipe_閉じれば読み待ちが解ける(t *testing.T) {
	tr := testTransport()
	tr.once.Do(func() {})
	srv := serverPipe(t, tr.Describe(DataPipe), windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE)
	go windows.ConnectNamedPipe(srv, nil)
	c, err := tr.Dial(DataPipe)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.Read(make([]byte, 188))
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	c.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Error("読みが成功してしまった")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("閉じても読み待ちが解けない")
	}
}

func TestWindowsPipe_相手が閉じればEOF(t *testing.T) {
	tr := testTransport()
	tr.once.Do(func() {})
	srv := serverPipe(t, tr.Describe(DataPipe), windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE)
	go func() {
		windows.ConnectNamedPipe(srv, nil)
		var w uint32
		windows.WriteFile(srv, []byte{0x47, 1, 2}, &w, nil)
		windows.FlushFileBuffers(srv) // 読み切られるまで待つ。切る前に捨てられないように
		windows.DisconnectNamedPipe(srv)
	}()
	c, err := tr.Dial(DataPipe)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	b, err := io.ReadAll(c)
	if len(b) != 3 || (err != nil && !isBroken(err)) {
		t.Errorf("% x %v", b, err)
	}
}

func TestWindowsPipe_繋がらなければ理由(t *testing.T) {
	tr := testTransport()
	tr.once.Do(func() {})
	if _, err := tr.Dial(CardPipe); err == nil || err.Error() != `DriverHost_PX4 に繋がりません (\\.\pipe\px4_card_pipe)` {
		t.Errorf("%v", err)
	}
}

func TestWindowsPipe_本物のパイプ名(t *testing.T) {
	tr := NewTransport("", "")
	for p, want := range map[Pipe]string{
		CtrlPipe: `\\.\pipe\px4_ctrl_pipe`, DataPipe: `\\.\pipe\px4_data_pipe`, CardPipe: `\\.\pipe\px4_card_pipe`,
	} {
		if got := tr.Describe(p); got != want {
			t.Errorf("%v: %q (want %q)", p, got, want)
		}
	}
}
