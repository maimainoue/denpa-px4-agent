package winscard

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// fakeAPI は 1 つの DLL に当たる偽の PC/SC。
type fakeAPI struct {
	mu        sync.Mutex
	readers   []string
	listErr   error
	ctxErr    error
	connErr   error
	next      uintptr
	contexts  map[uintptr]bool
	cards     map[uintptr]bool
	connected []string
	sent      [][]byte
}

func newFake(readers ...string) *fakeAPI {
	return &fakeAPI{readers: readers, contexts: map[uintptr]bool{}, cards: map[uintptr]bool{}}
}

func (f *fakeAPI) EstablishContext() (uintptr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ctxErr != nil {
		return 0, f.ctxErr
	}
	f.next++
	f.contexts[f.next] = true
	return f.next, nil
}

func (f *fakeAPI) ReleaseContext(ctx uintptr) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.contexts, ctx)
}

func (f *fakeAPI) ListReaders(ctx uintptr) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.readers, f.listErr
}

func (f *fakeAPI) Connect(ctx uintptr, reader string) (uintptr, uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.connErr != nil {
		return 0, 0, f.connErr
	}
	f.next++
	f.cards[f.next] = true
	f.connected = append(f.connected, reader)
	return f.next, 2, nil
}

func (f *fakeAPI) Transmit(card uintptr, proto uint32, apdu []byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, append([]byte(nil), apdu...))
	return append([]byte(nil), apdu...), nil
}

func (f *fakeAPI) Disconnect(card uintptr) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.cards, card)
}

func (f *fakeAPI) open() (contexts, cards int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.contexts), len(f.cards)
}

// loaderOf はパス → 偽の API (または読めない理由) の Loader。読んだ回数も数える。
func loaderOf(m map[string]*fakeAPI, loadErr map[string]error, loads map[string]int) Loader {
	var mu sync.Mutex
	return func(path string) (API, error) {
		mu.Lock()
		defer mu.Unlock()
		loads[path]++
		if err := loadErr[path]; err != nil {
			return nil, err
		}
		a, ok := m[path]
		if !ok {
			return nil, errors.New("見つかりません")
		}
		return a, nil
	}
}

type logs struct {
	mu sync.Mutex
	l  []string
}

func (g *logs) add(format string, args ...any) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.l = append(g.l, fmt.Sprintf(format, args...))
}

func listNames(t *testing.T, p *Provider) []string {
	t.Helper()
	rs, err := p.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var out []string
	for _, r := range rs {
		if r.Internal {
			t.Errorf("%s: 外付けのはずが Internal", r.Name)
		}
		out = append(out, r.Name)
	}
	return out
}

func TestListJoinsDLLsInOrderAndDropsDuplicates(t *testing.T) {
	a := newFake("Reader A", "Shared")
	b := newFake("Shared", "Reader B")
	loads := map[string]int{}
	p := New([]string{`C:\a\winscard.dll`, `C:\b\winscard.dll`}, loaderOf(map[string]*fakeAPI{`C:\a\winscard.dll`: a, `C:\b\winscard.dll`: b}, nil, loads), nil)

	got := listNames(t, p)
	if want := []string{"Reader A", "Shared", "Reader B"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("リーダー = %v, want %v", got, want)
	}

	// 重複は先の DLL (a) のもので開く
	l, err := p.Open("Shared")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if len(a.connected) != 1 || len(b.connected) != 0 {
		t.Errorf("開いた DLL: a=%v b=%v", a.connected, b.connected)
	}
	// 後の DLL にしか無いリーダーは b で開く
	l2, err := p.Open("Reader B")
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if len(b.connected) != 1 || b.connected[0] != "Reader B" {
		t.Errorf("b で開いていません: %v", b.connected)
	}
}

func TestListEmptyWhenNoReaders(t *testing.T) {
	a := newFake()
	p := New([]string{`C:\a.dll`}, loaderOf(map[string]*fakeAPI{`C:\a.dll`: a}, nil, map[string]int{}), nil)
	rs, err := p.List()
	if err != nil || len(rs) != 0 {
		t.Fatalf("rs=%v err=%v", rs, err)
	}
	if c, _ := a.open(); c != 0 {
		t.Errorf("context が残っています: %d", c)
	}
}

func TestBrokenDLLDoesNotStopOthersAndLogsOnce(t *testing.T) {
	good := newFake("Reader A")
	var g logs
	loads := map[string]int{}
	p := New([]string{`C:\bad.dll`, `C:\good.dll`},
		loaderOf(map[string]*fakeAPI{`C:\good.dll`: good}, map[string]error{`C:\bad.dll`: errors.New("壊れています")}, loads), g.add)

	for i := 0; i < 3; i++ {
		if got := listNames(t, p); !reflect.DeepEqual(got, []string{"Reader A"}) {
			t.Fatalf("リーダー = %v", got)
		}
	}
	if len(g.l) != 1 || !strings.Contains(g.l[0], `C:\bad.dll`) || !strings.Contains(g.l[0], "壊れています") {
		t.Errorf("ログは 1 度だけ、パスと理由つき: %v", g.l)
	}
	// 読めなかった DLL は、次の List でまた試す (あとから置かれたものを拾う)
	if loads[`C:\bad.dll`] != 3 {
		t.Errorf("読み直しの回数 = %d, want 3", loads[`C:\bad.dll`])
	}
}

func TestAllDLLsFailReturnsError(t *testing.T) {
	var g logs
	p := New([]string{`C:\x.dll`, `C:\y.dll`}, loaderOf(nil, map[string]error{
		`C:\x.dll`: errors.New("だめ1"), `C:\y.dll`: errors.New("だめ2"),
	}, map[string]int{}), g.add)
	_, err := p.List()
	if err == nil {
		t.Fatal("全部失敗したのにエラーになりません")
	}
	for _, s := range []string{`C:\x.dll`, `C:\y.dll`, "だめ1", "だめ2"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("エラーに %q がありません: %v", s, err)
		}
	}
}

func TestServiceErrorIsReportedWithName(t *testing.T) {
	a := newFake()
	a.ctxErr = &Error{Op: "SCardEstablishContext", RV: RVNoService}
	p := New([]string{`C:\a.dll`}, loaderOf(map[string]*fakeAPI{`C:\a.dll`: a}, nil, map[string]int{}), nil)
	_, err := p.List()
	var e *Error
	if !errors.As(err, &e) || e.RV != RVNoService {
		t.Fatalf("*Error を取り出せません: %v", err)
	}
	if !strings.Contains(err.Error(), "SCARD_E_NO_SERVICE") {
		t.Errorf("返り値の名前が出ません: %v", err)
	}
}

func TestOpenTransmitCloseReleasesEverything(t *testing.T) {
	a := newFake("Reader A")
	p := New([]string{`C:\a.dll`}, loaderOf(map[string]*fakeAPI{`C:\a.dll`: a}, nil, map[string]int{}), nil)
	listNames(t, p)
	l, err := p.Open("Reader A")
	if err != nil {
		t.Fatal(err)
	}
	if c, k := a.open(); c != 1 || k != 1 {
		t.Fatalf("開いている context=%d card=%d", c, k)
	}
	resp, err := l.Transmit([]byte{0x90, 0x32, 0, 0})
	if err != nil || !reflect.DeepEqual(resp, []byte{0x90, 0x32, 0, 0}) {
		t.Fatalf("resp=%x err=%v", resp, err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if c, k := a.open(); c != 0 || k != 0 {
		t.Errorf("閉じたあとに残っています context=%d card=%d", c, k)
	}
	if err := l.Close(); err != nil {
		t.Errorf("2 度目の Close: %v", err)
	}
	if _, err := l.Transmit([]byte{1}); err == nil {
		t.Error("閉じたあとの Transmit が通りました")
	}
}

func TestOpenFailureReleasesContext(t *testing.T) {
	a := newFake("Reader A")
	a.connErr = &Error{Op: "SCardConnect", RV: RVNoSmartcard}
	p := New([]string{`C:\a.dll`}, loaderOf(map[string]*fakeAPI{`C:\a.dll`: a}, nil, map[string]int{}), nil)
	listNames(t, p)
	_, err := p.Open("Reader A")
	var e *Error
	if !errors.As(err, &e) || e.RV != RVNoSmartcard {
		t.Fatalf("err = %v", err)
	}
	if c, _ := a.open(); c != 0 {
		t.Errorf("context が残っています: %d", c)
	}
}

func TestOpenBeforeListTriesDLLsInOrder(t *testing.T) {
	a := newFake()
	a.connErr = &Error{Op: "SCardConnect", RV: 0x80100009}
	b := newFake()
	p := New([]string{`C:\a.dll`, `C:\b.dll`}, loaderOf(map[string]*fakeAPI{`C:\a.dll`: a, `C:\b.dll`: b}, nil, map[string]int{}), nil)
	l, err := p.Open("Reader B")
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	if len(b.connected) != 1 {
		t.Errorf("b で開いていません: %v", b.connected)
	}
}

func TestNewDedupesPathsCaseInsensitively(t *testing.T) {
	loads := map[string]int{}
	a := newFake("R")
	p := New([]string{`C:\Windows\System32\winscard.dll`, `c:/windows/system32/WINSCARD.DLL`, ""},
		loaderOf(map[string]*fakeAPI{`C:\Windows\System32\winscard.dll`: a}, nil, loads), nil)
	if len(p.libs) != 1 {
		t.Fatalf("DLL の数 = %d, want 1", len(p.libs))
	}
	listNames(t, p)
	if len(loads) != 1 {
		t.Errorf("読んだ DLL = %v", loads)
	}
}

func TestErrorFormat(t *testing.T) {
	if got := (&Error{Op: "SCardConnect", RV: RVSharing}).Error(); !strings.Contains(got, "SCARD_E_SHARING_VIOLATION") || !strings.Contains(got, "0x8010000B") {
		t.Errorf("%s", got)
	}
	if got := (&Error{Op: "x", RV: 0x12345678}).Error(); !strings.Contains(got, "0x12345678") {
		t.Errorf("%s", got)
	}
}
