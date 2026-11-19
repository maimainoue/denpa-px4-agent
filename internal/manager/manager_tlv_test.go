package manager

import (
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
)

// tlvTuner は TLV (先頭が 0x7F) を流す偽の機材。
type tlvTuner struct{ fakeTuner }

type tlvReader struct{ stop <-chan struct{} }

func (r *tlvReader) Read(p []byte) (int, error) {
	select {
	case <-r.stop:
		return 0, io.EOF
	case <-time.After(2 * time.Millisecond):
	}
	n := min(188, len(p))
	clear(p[:n])
	p[0] = 0x7F
	return n, nil
}
func (r *tlvReader) Close() error { return nil }

func (f *tlvTuner) Start() (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &tlvReader{stop: f.stopCh}, nil
}

// countProc は通ったバイト数と、SetDecrypt の最後の値を数える復号。
type countProc struct {
	bytes   atomic.Int64
	decrypt atomic.Bool
}

func (c *countProc) Process(in []byte) []byte { c.bytes.Add(int64(len(in))); return in }
func (c *countProc) SetDecrypt(on bool)       { c.decrypt.Store(on) }
func (c *countProc) Close()                   {}

func TestPump_TLVはTLV用の復号を通し_TSは通さない(t *testing.T) {
	var tsProcs, tlvProcs atomic.Int32
	tlv := &countProc{}
	m := New(Config{
		Log: func(string, ...any) {},
		Open: func(spec backend.TunerSpec) (backend.Tuner, error) {
			return &tlvTuner{fakeTuner{name: spec.Name, types: spec.Types}}, nil
		},
		NewProcessor:    func(string) Processor { tsProcs.Add(1); return &countProc{} },
		NewTLVProcessor: func(string) Processor { tlvProcs.Add(1); return tlv },
		Grace:           150 * time.Millisecond,
		FirstTS:         2 * time.Second,
	})
	m.SetSpecs([]backend.TunerSpec{spec("T", "CATV")}, false)
	t.Cleanup(m.Shutdown)

	s, err := m.Open(Request{Type: "CATV", Channel: "QC30", Priority: 10, Use: "live", Service: 1})
	if err != nil {
		t.Fatal(err)
	}
	if b := readOne(t, s); len(b) == 0 || b[0] != 0x7F {
		t.Errorf("TLV がそのまま流れるはず: %x", b[:1])
	}
	if tlvProcs.Load() != 1 || tsProcs.Load() != 0 {
		t.Errorf("TLV 用 %d・TS 用 %d (TLV 用だけのはず)", tlvProcs.Load(), tsProcs.Load())
	}
	if tlv.bytes.Load() == 0 {
		t.Error("TLV の復号を通っていない")
	}
}
