//go:build hostlive && windows

package host

// 実機の DriverHost_PX4 (Windows) に繋ぐ試験。普段は走らない。
//
//	go test -tags hostlive -run Live -v ./internal/host
//
// HOST_PATH に DriverHost_PX4.exe、HOST_GUID に機種の GUID、HOST_CH に確かめるチャンネルを渡す。

import (
	"fmt"
	"io"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/devices"
)

func liveTuner(t *testing.T) backend.Tuner {
	path, guid := os.Getenv("HOST_PATH"), os.Getenv("HOST_GUID")
	if path == "" || guid == "" {
		t.Skip("HOST_PATH と HOST_GUID が要ります")
	}
	defs := devices.NewDefs()
	defs.Put(devices.Def{Model: "live", Host: &devices.Host{DeviceGUID: guid}, Receivers: []devices.Receiver{{Index: 0, Types: []string{"GR"}}}, LNB: []int{}})
	tn, err := Open(defs, NewTransport("", path), func(f string, a ...any) { t.Logf(f, a...) }, backend.TunerSpec{Name: "live #0", Device: DevicePrefix + guid + ":0:1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tn.Close() })
	return tn
}

// pids は TS の PID ごとの数と、同期バイトの乱れ。
func pids(b []byte) (map[uint16]int, int) {
	m := map[uint16]int{}
	bad := 0
	for i := 0; i+188 <= len(b); i += 188 {
		if b[i] != 0x47 {
			bad++
			continue
		}
		m[uint16(b[i+1]&0x1F)<<8|uint16(b[i+2])]++
	}
	return m, bad
}

func readFor(t *testing.T, tn backend.Tuner, d time.Duration) []byte {
	rd, err := tn.Start()
	if err != nil {
		t.Fatal(err)
	}
	var all []byte
	buf := make([]byte, 188*1024)
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) && len(all) < 8<<20 {
		type res struct {
			n   int
			err error
		}
		ch := make(chan res, 1)
		go func() { n, err := rd.Read(buf); ch <- res{n, err} }()
		select {
		case r := <-ch:
			all = append(all, buf[:r.n]...)
			if r.err != nil {
				t.Logf("読み出しが終わりました (%d バイトのあと): %v", len(all), r.err)
				return all
			}
		case <-time.After(2 * time.Second):
			t.Logf("2 秒読めません (%d バイト)", len(all))
			return all
		}
	}
	return all
}

func summarize(b []byte) string {
	m, bad := pids(b)
	type kv struct {
		pid uint16
		n   int
	}
	var list []kv
	for p, n := range m {
		list = append(list, kv{p, n})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].n > list[j].n })
	s := fmt.Sprintf("%d バイト 同期の乱れ=%d PID:", len(b), bad)
	for i, e := range list {
		if i >= 8 {
			break
		}
		s += fmt.Sprintf(" 0x%04X×%d", e.pid, e.n)
	}
	return s
}

func TestLive_選局と再選局(t *testing.T) {
	tn := liveTuner(t)
	ch := os.Getenv("HOST_CH")
	if ch == "" {
		ch = "T16"
	}
	for i := 0; i < 4; i++ {
		began := time.Now()
		err := tn.Tune(backend.Tuning{Type: "GR", Channel: ch})
		t.Logf("[%d] Tune(%s): %v (%.2f 秒)", i, ch, err, time.Since(began).Seconds())
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
		b := readFor(t, tn, 1500*time.Millisecond)
		t.Logf("[%d] %s", i, summarize(b))
		if sig, _ := tn.Signal(); sig != nil {
			cn := -1.0
			if sig.CN != nil {
				cn = *sig.CN
			}
			t.Logf("[%d] signal locked=%v cn=%.2f", i, sig.Locked, cn)
		}
		if lt, ok := tn.(*Tuner); ok && i == 0 {
			for _, ty := range []uint32{statSignalStrength, statCNR} {
				resp, err := lt.call(statsReq(ty))
				t.Logf("READ_STATS(%d): %x %v", ty, resp, err)
			}
		}
		tn.Stop()
		t.Logf("[%d] Stop", i)
	}
	_ = io.EOF
}
