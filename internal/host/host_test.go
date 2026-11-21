package host

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/devices"
)

func TestLayout_構造体の大きさは仕様書のとおり(t *testing.T) {
	cases := []struct {
		name string
		l    Layout
		want [3]int // ReceiverInfo, Open, Card
	}{
		{"Windows", LayoutWindows, [3]int{428, 436, 4952}},
		{"macOS", LayoutMac, [3]int{812, 820, 9304}},
	}
	for _, c := range cases {
		got := [3]int{c.l.ReceiverInfoSize(), c.l.OpenSize(), c.l.CardSize()}
		if got != c.want {
			t.Errorf("%s: %v (want %v)", c.name, got, c.want)
		}
	}
	// 固定長の制御コマンド
	for name, got := range map[string][2]int{
		"GET_VERSION": {len(versionReq()), 16}, "CLOSE": {len(closeReq()), 8}, "SET_CAPTURE": {len(captureReq(true)), 12},
		"SET_PARAMS(0)": {len(paramsReq(0x10, 1, nil)), 28}, "SET_PARAMS(1)": {len(paramsReq(0x20, 1, []Param{{16, 1}})), 28},
		"SET_PARAMS(2)": {len(paramsReq(0x40, 1, []Param{{1, 6}, {2, 0}})), 36}, "TUNE": {len(tuneReq(1)), 12},
		"CHECK_LOCK": {len(checkLockReq()), 12}, "SET_LNB_VOLTAGE": {len(lnbReq(15)), 12}, "READ_STATS": {len(statsReq(2)), 20},
	} {
		if got[0] != got[1] {
			t.Errorf("%s: %d (want %d)", name, got[0], got[1])
		}
	}
	if captureReq(true)[8] != 1 {
		t.Error("bool capture は 9 バイト目")
	}
}

func TestGUID(t *testing.T) {
	g, err := GUID("{aa8de3fc-004d-49a4-b976-4250f15a976d}")
	want := []byte{0xfc, 0xe3, 0x8d, 0xaa, 0x4d, 0x00, 0xa4, 0x49, 0xb9, 0x76, 0x42, 0x50, 0xf1, 0x5a, 0x97, 0x6d}
	if err != nil || !bytes.Equal(g[:], want) {
		t.Errorf("% x %v", g, err)
	}
	if _, err := GUID("zzz"); err == nil {
		t.Error("読めない GUID は断る")
	}
}

func TestWChars(t *testing.T) {
	for _, l := range []Layout{LayoutWindows, LayoutMac} {
		b := make([]byte, 32*l.W)
		putWChars(b, "PLEX PX-MLT5PE カード", l.W)
		if got := getWChars(b, l.W); got != "PLEX PX-MLT5PE カード" {
			t.Errorf("W=%d: %q", l.W, got)
		}
	}
}

// ---- 偽の DriverHost_PX4 ----

type fakeHost struct {
	l         Layout
	mu        sync.Mutex
	ctrl      [][]byte
	data      [][]byte
	cardReqs  [][]byte
	failTune  bool
	failTunes int // TUNE を最初の何回だけ失敗させる (0 は失敗させない)
	failOpen  bool
	ts        []byte
	cnrMdB    int32
	readers   []string
	dataID    uint32
}

type fakeTr struct{ h *fakeHost }

func (t *fakeTr) Layout() Layout         { return t.h.l }
func (t *fakeTr) Describe(p Pipe) string { return "fake" }
func (t *fakeTr) Dial(p Pipe) (Conn, error) {
	c1, c2 := net.Pipe()
	go t.h.serve(p, c2)
	return c1, nil
}

func (h *fakeHost) ctrlSize(c net.Conn, hdr []byte) ([]byte, bool) {
	cmd := binary.LittleEndian.Uint32(hdr)
	var n int
	switch cmd {
	case cmdGetVersion:
		n = 16
	case cmdOpen:
		n = h.l.OpenSize()
	case cmdClose, cmdClearParams:
		n = 8
	case cmdSetCapture, cmdTune, cmdCheckLock, cmdSetLnbVolt:
		n = 12
	case cmdReadStats:
		n = 20
	case cmdSetParams:
		rest := make([]byte, 12)
		if _, err := io.ReadFull(c, rest); err != nil {
			return nil, false
		}
		num := int(binary.LittleEndian.Uint32(rest[8:]))
		n = 28
		if num > 1 {
			n += 8 * (num - 1)
		}
		more := make([]byte, n-20)
		if _, err := io.ReadFull(c, more); err != nil {
			return nil, false
		}
		return append(append(append([]byte(nil), hdr...), rest...), more...), true
	default:
		return nil, false
	}
	b := make([]byte, n)
	copy(b, hdr)
	if _, err := io.ReadFull(c, b[8:]); err != nil {
		return nil, false
	}
	return b, true
}

func (h *fakeHost) serve(p Pipe, c net.Conn) {
	defer c.Close()
	switch p {
	case CtrlPipe:
		for {
			hdr := make([]byte, 8)
			if _, err := io.ReadFull(c, hdr); err != nil {
				return
			}
			req, ok := h.ctrlSize(c, hdr)
			if !ok {
				return
			}
			h.mu.Lock()
			h.ctrl = append(h.ctrl, req)
			resp := append([]byte(nil), req...)
			status := uint32(statusOK)
			switch binary.LittleEndian.Uint32(req) {
			case cmdGetVersion:
				binary.LittleEndian.PutUint32(resp[12:], cmdVersionValue)
			case cmdOpen:
				if h.failOpen {
					status = statusFail
				}
				binary.LittleEndian.PutUint32(resp[len(resp)-4:], 77) // data_id
				h.dataID = 77
			case cmdTune:
				if h.failTune || h.failTunes > 0 {
					status = statusFail
					if h.failTunes > 0 {
						h.failTunes--
					}
				}
			case cmdCheckLock:
				resp[8] = 1
			case cmdReadStats:
				binary.LittleEndian.PutUint32(resp[8:], 1)
				binary.LittleEndian.PutUint32(resp[12:], statCNR)
				binary.LittleEndian.PutUint32(resp[16:], uint32(h.cnrMdB))
			}
			binary.LittleEndian.PutUint32(resp[4:], status)
			h.mu.Unlock()
			if _, err := c.Write(resp); err != nil {
				return
			}
		}
	case DataPipe:
		for {
			b := make([]byte, 8)
			if _, err := io.ReadFull(c, b); err != nil {
				return
			}
			h.mu.Lock()
			h.data = append(h.data, b)
			ts := h.ts
			h.mu.Unlock()
			if binary.LittleEndian.Uint32(b) == dataPurge && ts != nil {
				c.Write(ts) // PURGE のあとの 1 塊は捨てられる (BonDriver と同じ)
				c.Write(ts)
			}
		}
	case CardPipe:
		for {
			req := make([]byte, h.l.CardSize())
			if _, err := io.ReadFull(c, req); err != nil {
				return
			}
			h.mu.Lock()
			h.cardReqs = append(h.cardReqs, req)
			resp := append([]byte(nil), req...)
			o := h.l.cardOffsets()
			switch binary.LittleEndian.Uint32(req) {
			case cardGetVersion:
				binary.LittleEndian.PutUint32(resp[12:], cardVersionValue)
			case cardListReaders:
				binary.LittleEndian.PutUint32(resp[16:], uint32(len(h.readers)))
				for i, n := range h.readers {
					putWChars(resp[o.readers+i*128*h.l.W:o.readers+(i+1)*128*h.l.W], n, h.l.W)
				}
			case cardConnect:
				if getWChars(req[o.readerName:o.readerName+128*h.l.W], h.l.W) == "NOCARD" {
					binary.LittleEndian.PutUint32(resp[8:], 3) // NO_CARD
				}
			case cardTransmit:
				n := binary.LittleEndian.Uint32(req[o.dataLen:])
				reply := append([]byte{0, 0, 0, 0, 0x21, 0x00}, req[o.data:o.data+int(n)]...)
				copy(resp[o.data:], reply)
				binary.LittleEndian.PutUint32(resp[o.dataLen:], uint32(len(reply)))
			}
			binary.LittleEndian.PutUint32(resp[4:], statusOK)
			h.mu.Unlock()
			if _, err := c.Write(resp); err != nil {
				return
			}
		}
	}
}

func (h *fakeHost) cmds() []uint32 {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []uint32
	for _, r := range h.ctrl {
		out = append(out, binary.LittleEndian.Uint32(r))
	}
	return out
}

func (h *fakeHost) find(cmd uint32) []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.ctrl {
		if binary.LittleEndian.Uint32(r) == cmd {
			return r
		}
	}
	return nil
}

func newTuner(t *testing.T, l Layout, name string, device string) (backend.Tuner, *fakeHost) {
	t.Helper()
	h := &fakeHost{l: l}
	tn, err := Open(devices.Builtin(), &fakeTr{h}, nil, backend.TunerSpec{Name: name, Device: device})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tn.Close() })
	return tn, h
}

const guidW3U4 = "{aa8de3fc-004d-49a4-b976-4250f15a976d}"

func TestTune_衛星の手順(t *testing.T) {
	for _, l := range []Layout{LayoutWindows, LayoutMac} {
		tn, h := newTuner(t, l, "PLEX PX-W3U4 #0", "px4host:"+guidW3U4+":0:1")
		h.ts = bytes.Repeat([]byte{0x47}, 188*4)
		if err := tn.SetLnb(15); err != nil {
			t.Fatal(err)
		}
		if err := tn.Tune(backend.Tuning{Type: "BS", Channel: "BS15:16625"}); err != nil {
			t.Fatalf("W=%d: %v", l.W, err)
		}
		// BonDriver と同じく、受信を先に始めてから選局する
		want := []uint32{cmdGetVersion, cmdOpen, cmdSetCapture, cmdSetLnbVolt, cmdSetParams, cmdTune}
		if got := h.cmds(); len(got) != len(want) {
			t.Fatalf("W=%d 手順: %v", l.W, got)
		} else {
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("W=%d 手順: %v (want %v)", l.W, got, want)
					break
				}
			}
		}
		open := h.find(cmdOpen)
		if len(open) != l.OpenSize() {
			t.Errorf("OPEN の大きさ: %d", len(open))
		}
		guid, _ := GUID(guidW3U4)
		if !bytes.Equal(open[8+96*l.W:8+96*l.W+16], guid[:]) {
			t.Errorf("OPEN の device_guid")
		}
		sysOff := 8 + 96*l.W + 16 + 96*l.W + 16
		if binary.LittleEndian.Uint32(open[sysOff:]) != SystemS || int32(binary.LittleEndian.Uint32(open[sysOff+4:])) != 0 {
			t.Errorf("OPEN の systems と index")
		}
		if v := binary.LittleEndian.Uint32(h.find(cmdSetLnbVolt)[8:]); v != 15 {
			t.Errorf("LNB: %d", v)
		}
		p := h.find(cmdSetParams)
		if binary.LittleEndian.Uint32(p[8:]) != SystemS || binary.LittleEndian.Uint32(p[12:]) != 1318000 ||
			binary.LittleEndian.Uint32(p[16:]) != 1 || binary.LittleEndian.Uint32(p[20:]) != paramStreamID || binary.LittleEndian.Uint32(p[24:]) != 16625 {
			t.Errorf("SET_PARAMS: % x", p)
		}
		if binary.LittleEndian.Uint32(h.find(cmdTune)[8:]) != 5000 {
			t.Errorf("TUNE の timeout")
		}
		if h.find(cmdSetCapture)[8] != 1 {
			t.Errorf("SET_CAPTURE(true)")
		}
		// TS のパイプ: SET_DATA_ID のあと PURGE
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			h.mu.Lock()
			n := len(h.data)
			h.mu.Unlock()
			if n >= 2 {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		h.mu.Lock()
		if len(h.data) < 2 || binary.LittleEndian.Uint32(h.data[0]) != dataSetDataID || binary.LittleEndian.Uint32(h.data[0][4:]) != 77 ||
			binary.LittleEndian.Uint32(h.data[1]) != dataPurge {
			t.Errorf("TS のパイプ: %v", h.data)
		}
		h.mu.Unlock()
		rd, err := tn.Start()
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 188*4)
		if _, err := io.ReadFull(rd, buf); err != nil || buf[0] != 0x47 {
			t.Errorf("TS を読めない: %v", err)
		}
		tn.Close()
	}
}

func TestTune_地上波はLNBに触らず_追加パラメータ無し(t *testing.T) {
	tn, h := newTuner(t, LayoutMac, "PLEX PX-W3U4 #2", "px4host:"+guidW3U4+":2:1")
	if err := tn.Tune(backend.Tuning{Type: "GR", Channel: "T27"}); err != nil {
		t.Fatal(err)
	}
	if h.find(cmdSetLnbVolt) != nil {
		t.Error("衛星でない選局では給電を触らない")
	}
	p := h.find(cmdSetParams)
	if binary.LittleEndian.Uint32(p[8:]) != SystemT || binary.LittleEndian.Uint32(p[12:]) != 557143 || binary.LittleEndian.Uint32(p[16:]) != 0 {
		t.Errorf("SET_PARAMS: % x", p)
	}
	open := h.find(cmdOpen)
	sysOff := 8 + 96*4 + 16 + 96*4 + 16
	if binary.LittleEndian.Uint32(open[sysOff:]) != SystemT || binary.LittleEndian.Uint32(open[sysOff+4:]) != 2 {
		t.Errorf("OPEN の systems と index")
	}
}

func TestTune_選局し直すときは受信を止めずSET_PARAMSとTUNEだけ(t *testing.T) {
	tn, h := newTuner(t, LayoutMac, "x", "px4host:"+guidW3U4+":2:1")
	tn.Tune(backend.Tuning{Type: "GR", Channel: "T27"})
	tn.Stop()
	tn.Tune(backend.Tuning{Type: "GR", Channel: "T28"})
	got := h.cmds()
	want := []uint32{cmdGetVersion, cmdOpen, cmdSetCapture, cmdSetParams, cmdTune, cmdSetParams, cmdTune}
	if len(got) != len(want) {
		t.Fatalf("手順: %v (want %v)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("手順: %v (want %v)", got, want)
		}
	}
}

func TestTune_ロックしなければ同期しなかった(t *testing.T) {
	tn, h := newTuner(t, LayoutWindows, "x", "px4host:"+guidW3U4+":2:1")
	h.failTune = true
	if err := tn.Tune(backend.Tuning{Type: "GR", Channel: "T27"}); !errors.Is(err, backend.ErrNoLock) {
		t.Fatalf("got %v", err)
	}
}

func TestTune_OPENに失敗したら受信機を開けません(t *testing.T) {
	tn, h := newTuner(t, LayoutWindows, "PX #2", "px4host:"+guidW3U4+":2:1")
	h.failOpen = true
	err := tn.Tune(backend.Tuning{Type: "GR", Channel: "T27"})
	if err == nil || err.Error() != "PX #2: 受信機を開けません (DriverHost_PX4 に空きがありません)" {
		t.Fatalf("got %v", err)
	}
}

func TestTune_受けられない種別と給電できない電圧は機材に触る前に400(t *testing.T) {
	tn, h := newTuner(t, LayoutMac, "x", "px4host:"+guidW3U4+":2:1")
	if err := tn.Tune(backend.Tuning{Type: "BS", Channel: "BS15"}); backend.StatusOf(err) != 400 {
		t.Errorf("GR の受信機で BS: %v", err)
	}
	tn2, _ := newTuner(t, LayoutMac, "y", "px4host:"+guidW3U4+":0:1")
	if err := tn2.SetLnb(11); !errors.Is(err, backend.ErrUnsupported) {
		t.Errorf("11V は出せない: %v", err)
	}
	if len(h.cmds()) != 0 {
		t.Errorf("機材に触っている: %v", h.cmds())
	}
}

func TestSignal(t *testing.T) {
	tn, h := newTuner(t, LayoutMac, "x", "px4host:"+guidW3U4+":2:1")
	h.cnrMdB = 27300
	if sig, _ := tn.Signal(); sig != nil {
		t.Error("受けていなければ nil")
	}
	tn.Tune(backend.Tuning{Type: "GR", Channel: "T27"})
	sig, _ := tn.Signal()
	if sig == nil || !sig.Locked || sig.CN == nil || *sig.CN != 27.3 || sig.Unit != "dB" {
		t.Errorf("%+v", sig)
	}
}

func TestClose_手放す順(t *testing.T) {
	tn, h := newTuner(t, LayoutMac, "x", "px4host:"+guidW3U4+":0:1")
	tn.SetLnb(15)
	tn.Tune(backend.Tuning{Type: "BS", Channel: "BS15"})
	tn.Close()
	got := h.cmds()
	n := len(got)
	if n < 3 || got[n-3] != cmdSetCapture || got[n-2] != cmdSetLnbVolt || got[n-1] != cmdClose {
		t.Errorf("SET_CAPTURE(false) → SET_LNB_VOLTAGE(0) → CLOSE: %v", got)
	}
}

func TestOpen_deviceの検査(t *testing.T) {
	h := &fakeHost{l: LayoutMac}
	for _, dev := range []string{"px4host:", "px4host:zz:0:1", "px4host:" + guidW3U4 + ":x:1", "px4host:{00000000-0000-0000-0000-000000000000}:0:1", "px4host:" + guidW3U4 + ":9:1"} {
		if _, err := Open(devices.Builtin(), &fakeTr{h}, nil, backend.TunerSpec{Name: "x", Device: dev}); err == nil {
			t.Errorf("%q: 断るはず", dev)
		}
	}
}

func TestReceivable(t *testing.T) {
	tn, _ := newTuner(t, LayoutMac, "x", "px4host:"+guidW3U4+":0:1")
	r := tn.(backend.Receivabler).Receivable()
	if len(r) != 2 || r[0] != "BS" || r[1] != "CS" {
		t.Errorf("%v", r)
	}
}

// ---- カードのパイプ ----

func TestCardProvider(t *testing.T) {
	for _, l := range []Layout{LayoutWindows, LayoutMac} {
		h := &fakeHost{l: l, readers: []string{"PLEX PX-MLT5PE Smart Card Reader (ID: 0001)", "NOCARD"}}
		p := NewCardProvider(&fakeTr{h})
		rs, err := p.List()
		if err != nil || len(rs) != 2 || rs[0].Name != "PLEX PX-MLT5PE Smart Card Reader (ID: 0001)" || !rs[0].Internal {
			t.Fatalf("W=%d: %+v %v", l.W, rs, err)
		}
		if _, err := p.Open("NOCARD"); err == nil || err.Error() != "カードのパイプがエラーを返しました (NO_CARD)" {
			t.Errorf("カードが無ければ NO_CARD: %v", err)
		}
		link, err := p.Open(rs[0].Name)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := link.Transmit([]byte{0x90, 0x30, 0, 0, 0})
		if err != nil || !bytes.Equal(resp[6:], []byte{0x90, 0x30, 0, 0, 0}) || resp[4] != 0x21 {
			t.Errorf("TRANSMIT: % x %v", resp, err)
		}
		h.mu.Lock()
		last := h.cardReqs[len(h.cardReqs)-1]
		h.mu.Unlock()
		if len(last) != l.CardSize() {
			t.Errorf("カードのコマンドの大きさ: %d", len(last))
		}
		o := l.cardOffsets()
		if binary.LittleEndian.Uint32(h.cardReqs[3][o.shareMode:]) != cardExclusive {
			t.Errorf("排他で開く")
		}
		link.Close()
	}
}

func TestCardProvider_繋がらなければ理由(t *testing.T) {
	p := NewCardProvider(deadTr{})
	_, err := p.List()
	var nc *ErrNotConnected
	if !errors.As(err, &nc) || err.Error() != "DriverHost_PX4 に繋がりません (/tmp/px4_card_pipe.sock)" {
		t.Errorf("%v", err)
	}
}

type deadTr struct{}

func (deadTr) Layout() Layout { return LayoutMac }
func (deadTr) Describe(p Pipe) string {
	return "/tmp/px4_card_pipe.sock"
}
func (d deadTr) Dial(p Pipe) (Conn, error) { return nil, &ErrNotConnected{Pipe: d.Describe(p)} }

// DriverHost_PX4 は SET_CAPTURE(false) のあとも TS のパイプを閉じず、流れが止まるだけ。
// 読み手を外せば読み待ちは解け、TS のパイプは繋ぎ直さない (繋ぎ直すと切られる。実機で 1 回おきに失敗した)。
func TestStop_読み待ちを解き_TSのパイプは繋ぎ直さない(t *testing.T) {
	tn, h := newTuner(t, LayoutMac, "x", "px4host:"+guidW3U4+":2:1")
	if err := tn.Tune(backend.Tuning{Type: "GR", Channel: "T27"}); err != nil {
		t.Fatal(err)
	}
	rd, _ := tn.Start()
	done := make(chan error, 1)
	go func() { _, err := rd.Read(make([]byte, 188)); done <- err }()
	time.Sleep(100 * time.Millisecond)
	tn.Stop()
	select {
	case err := <-done:
		if err != io.EOF {
			t.Errorf("読み手を外したら EOF: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop しても TS のパイプの読み待ちが解けない")
	}
	h.ts = bytes.Repeat([]byte{0x47}, 188*2)
	if err := tn.Tune(backend.Tuning{Type: "GR", Channel: "T28"}); err != nil {
		t.Fatal(err)
	}
	rd2, _ := tn.Start()
	buf := make([]byte, 188*2)
	if _, err := io.ReadFull(rd2, buf); err != nil || buf[0] != 0x47 {
		t.Errorf("同じパイプで 2 回目の選局の TS を読めない: %v", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	setIDs := 0
	for _, d := range h.data {
		if binary.LittleEndian.Uint32(d) == dataSetDataID {
			setIDs++
		}
	}
	if setIDs != 1 {
		t.Errorf("SET_DATA_ID は 1 回だけ: %d 回", setIDs)
	}
}

func TestFeed_DriverHostが閉じたら読み手は機材が抜かれた扱い(t *testing.T) {
	c1, c2 := net.Pipe()
	f := newDataFeed(c1)
	r := newFeedReader(f, "x")
	go func() { c2.Write(bytes.Repeat([]byte{0x47}, 188)); c2.Close() }()
	buf := make([]byte, 188)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Read(buf); !errors.Is(err, backend.ErrUnplugged) {
		t.Errorf("got %v", err)
	}
}

func TestTune_SKYは同期しなければずらした周波数でもう一度(t *testing.T) {
	// 受信機 0 が SKY の機材。GUID は組み込みの PX-W3U4 のものを使う (fake なので中身は問わない)。
	defs := devices.NewDefs()
	defs.Put(devices.Def{
		Model:     "TEST SKY",
		Host:      &devices.Host{DeviceGUID: guidW3U4},
		Receivers: []devices.Receiver{{Index: 0, Types: []string{"SKY"}}},
	})
	newSkyTuner := func(failTunes int) (backend.Tuner, *fakeHost) {
		t.Helper()
		h := &fakeHost{l: LayoutWindows, failTunes: failTunes}
		h.ts = bytes.Repeat([]byte{0x47}, 188*4)
		tn, err := Open(defs, &fakeTr{h}, nil, backend.TunerSpec{Name: "TEST SKY #0", Device: "px4host:" + guidW3U4 + ":0:1"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { tn.Close() })
		return tn, h
	}
	// SET_PARAMS の周波数と TUNE の回数を数える。
	probe := func(h *fakeHost) (freqs []uint32, tunes int) {
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, r := range h.ctrl {
			switch binary.LittleEndian.Uint32(r) {
			case cmdSetParams:
				freqs = append(freqs, binary.LittleEndian.Uint32(r[12:]))
			case cmdTune:
				tunes++
			}
		}
		return freqs, tunes
	}

	// H11 はずらす対象: 1 回目が駄目でも、+1000 kHz でもう一度選局して成功する。
	tn, h := newSkyTuner(1)
	if err := tn.Tune(backend.Tuning{Type: "SKY", Channel: "H11"}); err != nil {
		t.Fatalf("H11: %v", err)
	}
	freqs, tunes := probe(h)
	want := []uint32{93250 + 10*6500, 93250 + 10*6500 + 1000}
	if len(freqs) != len(want) || tunes != 2 {
		t.Fatalf("H11: SET_PARAMS の周波数 %v、TUNE %d 回 (want %v、2 回)", freqs, tunes, want)
	}
	for i := range want {
		if freqs[i] != want[i] {
			t.Errorf("H11: SET_PARAMS[%d] の周波数 %d (want %d)", i, freqs[i], want[i])
		}
	}

	// H01 はずらす対象でない: 1 回駄目ならそのまま同期しなかった。
	tn, h = newSkyTuner(1)
	if err := tn.Tune(backend.Tuning{Type: "SKY", Channel: "H01"}); !errors.Is(err, backend.ErrNoLock) {
		t.Errorf("H01: %v", err)
	}
	if freqs, tunes := probe(h); len(freqs) != 1 || freqs[0] != 93250 || tunes != 1 {
		t.Errorf("H01: SET_PARAMS の周波数 %v、TUNE %d 回 (want [93250]、1 回)", freqs, tunes)
	}

	// H11 でも 2 回駄目なら同期しなかった。
	tn, h = newSkyTuner(2)
	if err := tn.Tune(backend.Tuning{Type: "SKY", Channel: "H11"}); !errors.Is(err, backend.ErrNoLock) {
		t.Errorf("H11 2 回駄目: %v", err)
	}
	if _, tunes := probe(h); tunes != 2 {
		t.Errorf("H11 2 回駄目: TUNE %d 回 (want 2 回)", tunes)
	}
}
