package manager

import (
	"io"
	"sync"
)

// byteq は塊の列。上限を超えたら push が false を返す。
// 利用者ごとの列 (64MB) と、読み出しと復号の間の列で使う。
type byteq struct {
	mu     sync.Mutex
	cond   *sync.Cond
	q      [][]byte
	n      int64
	limit  int64
	closed bool
	err    error
}

func newByteq(limit int64) *byteq {
	q := &byteq{limit: limit}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// push は列に足す。閉じていたり、上限を超えるなら false。
func (q *byteq) push(b []byte) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || q.n+int64(len(b)) > q.limit {
		return false
	}
	q.q = append(q.q, b)
	q.n += int64(len(b))
	q.cond.Signal()
	return true
}

// pop は 1 塊取る。空なら待つ。閉じたあとは、残りを返し切ってから理由 (既定 io.EOF) を返す。
func (q *byteq) pop() ([]byte, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.q) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.q) > 0 {
		b := q.q[0]
		q.q[0] = nil
		q.q = q.q[1:]
		q.n -= int64(len(b))
		return b, nil
	}
	if q.err != nil {
		return nil, q.err
	}
	return nil, io.EOF
}

// finish は列を閉じる。drain が false なら溜まっているぶんを捨てる (壊す)。
func (q *byteq) finish(err error, drain bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	q.err = err
	if !drain {
		q.q, q.n = nil, 0
	}
	q.cond.Broadcast()
}
