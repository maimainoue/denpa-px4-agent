package host

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
)

// dataFeed は TS のパイプを読み続ける goroutine と、その結果の列。
//
// DriverHost_PX4 は SET_DATA_ID を書いたパイプの接続を受信機の使い終わりまで保つ前提で、
// 切って繋ぎ直すと切られる (実機で、繋ぎ直した選局が 1 回おきに失敗した)。
// そのため TS のパイプは開いたままにし、選局のたびの読み手は feedReader が受け持つ。
// 読み手を外しても、パイプの読み待ちは続く (SET_CAPTURE(false) のあとは流れが止まるだけ)。
type dataFeed struct {
	conn Conn
	ch   chan []byte
	dead chan struct{}
	skip atomic.Int32 // PURGE のあとに捨てる塊の数 (BonDriver の NumberOfBuffersToIgnoreAfterPurge)
}

const feedDepth = 256

func newDataFeed(c Conn) *dataFeed {
	f := &dataFeed{conn: c, ch: make(chan []byte, feedDepth), dead: make(chan struct{})}
	go f.loop()
	return f
}

func (f *dataFeed) loop() {
	defer close(f.dead)
	scratch := make([]byte, 188*1024) // 使い回す。列に入れるのは、読めたぶんだけの配列
	for {
		n, err := f.conn.Read(scratch)
		if n > 0 {
			if f.skip.Load() > 0 {
				f.skip.Add(-1)
			} else {
				f.push(append(make([]byte, 0, n), scratch[:n]...))
			}
		}
		if err != nil {
			return
		}
	}
}

// push は列に足す。読み手がいなくて溢れたら、古いほうを捨てる。
func (f *dataFeed) push(b []byte) {
	for {
		select {
		case f.ch <- b:
			return
		default:
			select {
			case <-f.ch:
			default:
			}
		}
	}
}

func (f *dataFeed) drain() {
	for {
		select {
		case <-f.ch:
		default:
			return
		}
	}
}

func (f *dataFeed) isDead() bool {
	select {
	case <-f.dead:
		return true
	default:
		return false
	}
}

func (f *dataFeed) close() { f.conn.Close() }

// feedReader は選局 1 回ぶんの読み手。stop で読み待ちが解ける。
type feedReader struct {
	f    *dataFeed
	name string
	stop chan struct{}
	once sync.Once
	left []byte
}

func newFeedReader(f *dataFeed, name string) *feedReader {
	return &feedReader{f: f, name: name, stop: make(chan struct{})}
}

func (r *feedReader) halt() { r.once.Do(func() { close(r.stop) }) }

func (r *feedReader) Read(p []byte) (int, error) {
	if len(r.left) == 0 {
		select {
		case b := <-r.f.ch:
			r.left = b
		case <-r.stop:
			return 0, io.EOF
		case <-r.f.dead:
			select { // 読み切ってから終わる
			case b := <-r.f.ch:
				r.left = b
			default:
				return 0, fmt.Errorf("%s: %w", r.name, backend.ErrUnplugged)
			}
		}
	}
	n := copy(p, r.left)
	r.left = r.left[n:]
	return n, nil
}

func (r *feedReader) Close() error { r.halt(); return nil }

var _ = errors.New
