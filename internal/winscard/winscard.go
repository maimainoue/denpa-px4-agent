// Package winscard は OS の PC/SC (Windows の winscard.dll) を使う card.Provider (仕様書 9.1)。
//
// DLL は 1 つに限らない。System32 の winscard.dll のほかに、設定 (WINSCARD_DLLS) で絶対パスを指した DLL を
// 足せる。DLL ごとに別の PC/SC の世界 (context) なので、リーダーは DLL をまたいで名前で見分ける。
// 同じ名前が複数の DLL に出たら、先に並べた DLL のものを使う。
//
// DLL を呼ぶ部分は API の向こうに置く。ここは OS に依らず、偽の API で試せる (DLL を読むのは dll_windows.go)。
package winscard

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/card"
)

// API は PC/SC の呼び出し。1 つの DLL に当たる。エラーは *Error (返り値つき) か、ふつうの error。
type API interface {
	EstablishContext() (ctx uintptr, err error)
	ReleaseContext(ctx uintptr)
	// ListReaders はリーダーの名前を並べる。1 つも無ければ空 (エラーにしない)。
	ListReaders(ctx uintptr) ([]string, error)
	// Connect は排他 (SCARD_SHARE_EXCLUSIVE)・T=1 で開く。
	Connect(ctx uintptr, reader string) (card uintptr, proto uint32, err error)
	// Transmit は APDU を送って応答 (SW1 SW2 込み) を受ける。
	Transmit(card uintptr, proto uint32, apdu []byte) ([]byte, error)
	Disconnect(card uintptr)
}

// Loader は DLL を読んで API にする。
type Loader func(path string) (API, error)

// 返り値 (winerror.h)。
const (
	RVOK             = 0x00000000
	RVSharing        = 0x8010000B
	RVNoSmartcard    = 0x8010000C
	RVNoService      = 0x8010001D
	RVServiceStopped = 0x8010001E
	RVNoReaders      = 0x8010002E
	RVUnresponsive   = 0x80100066
	RVRemovedCard    = 0x80100069
)

var rvNames = map[uint32]string{
	0x80100002:       "SCARD_E_CANCELLED",
	0x80100003:       "SCARD_E_INVALID_HANDLE",
	0x80100008:       "SCARD_E_INSUFFICIENT_BUFFER",
	0x80100009:       "SCARD_E_UNKNOWN_READER",
	0x8010000A:       "SCARD_E_TIMEOUT",
	RVSharing:        "SCARD_E_SHARING_VIOLATION",
	RVNoSmartcard:    "SCARD_E_NO_SMARTCARD",
	0x8010000F:       "SCARD_E_PROTO_MISMATCH",
	0x80100010:       "SCARD_E_NOT_READY",
	0x80100017:       "SCARD_E_READER_UNAVAILABLE",
	RVNoService:      "SCARD_E_NO_SERVICE",
	RVServiceStopped: "SCARD_E_SERVICE_STOPPED",
	RVNoReaders:      "SCARD_E_NO_READERS_AVAILABLE",
	RVUnresponsive:   "SCARD_W_UNRESPONSIVE_CARD",
	0x80100067:       "SCARD_W_UNPOWERED_CARD",
	0x80100068:       "SCARD_W_RESET_CARD",
	RVRemovedCard:    "SCARD_W_REMOVED_CARD",
}

// Error は PC/SC の返り値が成功でなかったもの。
type Error struct {
	Op string
	RV uint32
}

func (e *Error) Error() string {
	if n, ok := rvNames[e.RV]; ok {
		return fmt.Sprintf("%s: %s (0x%08X)", e.Op, n, e.RV)
	}
	return fmt.Sprintf("%s: 返り値 0x%08X", e.Op, e.RV)
}

type lib struct {
	path string
	mu   sync.Mutex
	api  API // 読めたものだけ持つ。読めなければ次のときにまた試す
}

// Provider は 1 つ以上の DLL を束ねた card.Provider。
type Provider struct {
	libs []*lib
	load Loader
	log  func(format string, args ...any)

	mu      sync.Mutex
	owner   map[string]*lib // List で見つけたリーダー → 持ち主の DLL
	lastErr map[string]string
}

var _ card.Provider = (*Provider)(nil)

// New は paths の順に DLL を並べて Provider を作る。同じパス (大文字小文字を区別しない) は 1 つにする。
// DLL は List・Open のときに読む。読めない DLL は、その理由を log に出し (同じ理由は 1 度だけ)、ほかを止めない。
func New(paths []string, load Loader, log func(format string, args ...any)) *Provider {
	p := &Provider{load: load, log: log, owner: map[string]*lib{}, lastErr: map[string]string{}}
	seen := map[string]bool{}
	for _, s := range paths {
		k := strings.ToLower(strings.ReplaceAll(s, "/", `\`))
		if s == "" || seen[k] {
			continue
		}
		seen[k] = true
		p.libs = append(p.libs, &lib{path: s})
	}
	return p
}

func (p *Provider) logf(format string, args ...any) {
	if p.log != nil {
		p.log(format, args...)
	}
}

// api は DLL を (まだなら) 読む。
func (p *Provider) api(l *lib) (API, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.api != nil {
		return l.api, nil
	}
	a, err := p.load(l.path)
	if err != nil {
		return nil, fmt.Errorf("PC/SC の DLL を読めません (%s): %w", l.path, err)
	}
	l.api = a
	return a, nil
}

// failed は DLL ごとの失敗を、同じ理由が続くあいだは 1 度だけログに出す。
func (p *Provider) failed(l *lib, err error) {
	p.mu.Lock()
	same := p.lastErr[l.path] == err.Error()
	p.lastErr[l.path] = err.Error()
	p.mu.Unlock()
	if !same {
		p.logf("[winscard] %v", err)
	}
}

func (p *Provider) recovered(l *lib) {
	p.mu.Lock()
	delete(p.lastErr, l.path)
	p.mu.Unlock()
}

func (p *Provider) list(l *lib) ([]string, error) {
	a, err := p.api(l)
	if err != nil {
		return nil, err
	}
	ctx, err := a.EstablishContext()
	if err != nil {
		return nil, fmt.Errorf("PC/SC に繋がりません (%s): %w", l.path, err)
	}
	defer a.ReleaseContext(ctx)
	names, err := a.ListReaders(ctx)
	if err != nil {
		return nil, fmt.Errorf("リーダーの一覧を読めません (%s): %w", l.path, err)
	}
	return names, nil
}

// List は DLL の順にリーダーを並べる。1 つでも読めた DLL があればその分を返し、
// 全部の DLL が失敗したときだけエラーにする。リーダー名は取ってきたものをそのまま使う。
func (p *Provider) List() ([]card.ReaderInfo, error) {
	var out []card.ReaderInfo
	var errs []error
	owner := map[string]*lib{}
	for _, l := range p.libs {
		names, err := p.list(l)
		if err != nil {
			p.failed(l, err)
			errs = append(errs, err)
			continue
		}
		p.recovered(l)
		for _, n := range names {
			if n == "" || owner[n] != nil {
				continue
			}
			owner[n] = l
			out = append(out, card.ReaderInfo{Name: n})
		}
	}
	p.mu.Lock()
	p.owner = owner
	p.mu.Unlock()
	if len(errs) > 0 && len(errs) == len(p.libs) {
		if len(errs) == 1 {
			return nil, errs[0]
		}
		return nil, &multiError{errs}
	}
	return out, nil
}

// multiError は DLL ごとの失敗を並べたもの。errors.As で中の *Error を取り出せる。
type multiError struct{ errs []error }

func (m *multiError) Error() string {
	var s []string
	for _, e := range m.errs {
		s = append(s, e.Error())
	}
	return strings.Join(s, "; ")
}

func (m *multiError) Unwrap() []error { return m.errs }

// Open はリーダーを排他・T=1 で開く。カードが無ければエラー。
// リーダーが出てきた DLL で開く。List の前なら、DLL の順に試す。
func (p *Provider) Open(name string) (card.Link, error) {
	p.mu.Lock()
	l := p.owner[name]
	p.mu.Unlock()
	if l != nil {
		return p.open(l, name)
	}
	var last error
	for _, l := range p.libs {
		k, err := p.open(l, name)
		if err == nil {
			return k, nil
		}
		last = err
	}
	if last == nil {
		last = errors.New("PC/SC の DLL がありません")
	}
	return nil, last
}

func (p *Provider) open(l *lib, name string) (card.Link, error) {
	a, err := p.api(l)
	if err != nil {
		return nil, err
	}
	ctx, err := a.EstablishContext()
	if err != nil {
		return nil, fmt.Errorf("PC/SC に繋がりません (%s): %w", l.path, err)
	}
	h, proto, err := a.Connect(ctx, name)
	if err != nil {
		a.ReleaseContext(ctx)
		return nil, fmt.Errorf("カードへの接続 (%s): %w", name, err)
	}
	return &link{a: a, ctx: ctx, card: h, proto: proto}, nil
}

// link は開いたカード。
type link struct {
	mu    sync.Mutex
	a     API
	ctx   uintptr
	card  uintptr
	proto uint32
	done  bool
}

func (k *link) Transmit(apdu []byte) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.done {
		return nil, errors.New("カードを閉じています")
	}
	return k.a.Transmit(k.card, k.proto, apdu)
}

func (k *link) Close() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.done {
		return nil
	}
	k.done = true
	k.a.Disconnect(k.card)
	k.a.ReleaseContext(k.ctx)
	return nil
}
