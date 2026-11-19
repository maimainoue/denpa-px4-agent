package api

import "sync"

// Hub は SSE の通知 (仕様書 10.2)。購読者ごとの列は 64 件で、溜まったら古いほうを捨てる。
type Hub struct {
	mu   sync.Mutex
	subs map[chan string]struct{}
}

// NewHub は空の Hub を作る。
func NewHub() *Hub { return &Hub{subs: map[chan string]struct{}{}} }

// Notify は通知を出す。止まらない。
func (h *Hub) Notify(event string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		for {
			select {
			case ch <- event:
			default:
				select {
				case <-ch: // 古いほうを捨てる
				default:
				}
				continue
			}
			break
		}
	}
}

// Subscribe は購読する。
func (h *Hub) Subscribe() (<-chan string, func()) {
	ch := make(chan string, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

// CloseAll は止まるとき、通知の接続をこちらから閉じる。
func (h *Hub) CloseAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		close(ch)
		delete(h.subs, ch)
	}
}
