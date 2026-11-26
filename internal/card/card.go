// Package card はカードリーダーと B-CAS (仕様書 9.1・9.2・9.4・9.6)。
// カードへの経路は Provider の向こう側 (pcscd・DriverHost_PX4 のカードのパイプ・OS の PC/SC) に置き、
// ここでは APDU を送って応答を受ける Link だけを見る。
package card

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/b25"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/b61"
)

// Link は開いたカード。APDU を送って応答を受ける。
type Link interface {
	Transmit(apdu []byte) ([]byte, error)
	Close() error
}

// ReaderInfo はリーダー 1 つ。名前は取ってきたものをそのまま使う。
type ReaderInfo struct {
	Name     string
	Internal bool
}

// Provider はカードへの経路 1 つ。
type Provider interface {
	// List はリーダーを並べる。繋がらなければ理由 (`pcscd に繋がりません (…)`) を返す。
	List() ([]ReaderInfo, error)
	// Open は排他で開く。カードが無ければエラー。
	Open(name string) (Link, error)
}

// Store は使わないリーダー (cards.json)。
type Store interface {
	Disabled() []string
	SetDisabled(names []string) ([]string, error)
}

// Hooks はチューナー側から貰う情報と、通知。
type Hooks struct {
	Active func() []string            // 復号中のチューナーの名前
	Device func() map[string][]string // 機材の中で復号するチューナー (名前 → 使っているもの)
	Notify func()                     // card の通知
}

const (
	retryBackoff = 10 * time.Second
	ecmTTL       = 5 * time.Second
	emmQueue     = 16
	emmInterval  = time.Second
	emmRemember  = 256
	notifyEvery  = time.Minute
)

type ecmEntry struct {
	res b25.ECMResult
	at  time.Time
}

type readerState struct {
	err string
}

// Manager は開いているカード 1 つと、そこへの問い合わせ。
type Manager struct {
	providers []Provider
	store     Store
	log       func(format string, args ...any)
	hooks     Hooks

	opMu sync.Mutex // カードへは同時に 1 つだけ送る

	mu        sync.Mutex
	link      Link
	cur       ReaderInfo
	info      b25.Info
	idStrs    []string
	haveInfo  bool
	acas      bool   // 開いているカードが A-CAS (B-CAS 互換モード)
	b61Module uint64 // 4K の INT で読んだ CAS モジュール ID (カードを手放すと忘れる。0 は未取得)
	failUntil time.Time
	failMsg   string
	states    map[string]*readerState
	ecm       map[string]ecmEntry

	emmQ       chan []byte
	emmOnce    sync.Once
	emmSeen    map[uint64]struct{}
	lastNotify time.Time

	remote         RemoteSource // 別の拠点から貰うときのカード。あればこちらを使います。
	emmUnsupported bool         // 配る側が古くて EMM を受け取れない。送るのを止めます。
}

// RemoteSource は別の拠点から鍵を貰うときのカードです (9.5)。cardshare.RemoteCard がこれです。
// LAN の中だけという前提です。
type RemoteSource interface {
	// Init はカードの定数。1 回受け取れば変わらないので控えておきます。
	Init() (b25.Info, error)
	// Check は配る側とやり取りできるかです。控えている定数を使わずに問い合わせ直します。
	Check() (b25.Info, error)
	// Ecm は ECM 1 つを送って鍵を受け取ります。
	Ecm(ecm []byte) (b25.ECMResult, error)
	// Confirm は契約確認です。verification は 1〜253 バイト、dateMJD は MJD です。
	Confirm(verification []byte, dateMJD uint16) (uint16, error)
	// Emm は契約の更新 (EMM) を配る側に渡し、配る側のカードの返り値を返します。
	Emm(payload []byte) (uint16, error)
	// URL は配る側の URL です。画面に出します。
	URL() string
}

// ErrEmmUnsupported は、鍵を配る拠点のエージェントが古くて EMM を受け取れないときの理由です。
// 鍵と契約確認はそのまま使います。
var ErrEmmUnsupported = errors.New("鍵を配る拠点のエージェントが古く、契約の更新 (EMM) を受け取れません。鍵と契約確認はそのまま使います")

// LocalCard は手元のカードだけを見るものです。配る側 (/denpa/card/init・/ecm・/confirm・/emm) が使います。
// Info・ECM・Confirm・EMMNow は Manager の手元のカードのものと同じです。
type LocalCard interface {
	Info() (b25.Info, error)
	ECM(ecm []byte) (b25.ECMResult, error)
	Confirm(ctx context.Context, verification []byte, mjd int) (int, error)
	EMMNow(payload []byte) (int, error)
}

// localCard は Manager の手元のカードだけを見る包みです。
type localCard struct{ m *Manager }

func (l localCard) Info() (b25.Info, error)               { return l.m.localInfo() }
func (l localCard) ECM(ecm []byte) (b25.ECMResult, error) { return l.m.localECM(ecm) }
func (l localCard) Confirm(ctx context.Context, v []byte, mjd int) (int, error) {
	return l.m.localConfirm(ctx, v, mjd)
}
func (l localCard) EMMNow(payload []byte) (int, error) { return l.m.EMMNow(payload) }

// SetRemote は別の拠点から鍵を貰うときのカードを入れます。
// 入れたら、手元のカードではなくこちらを使います。配る側は LocalCard() で手元のカードを使います。
func (m *Manager) SetRemote(remote RemoteSource) {
	m.mu.Lock()
	m.remote = remote
	m.mu.Unlock()
}

// LocalCard は手元のカードだけを返します。別の拠点から貰う設定でも、手元のカードを返します。
// 鍵を配るサーバが使います。貰った鍵をさらに配ることはしません。
func (m *Manager) LocalCard() LocalCard { return localCard{m} }

func (m *Manager) remoteSource() RemoteSource {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.remote
}

func cardIDString(id b25.CardID) string {
	var v uint64
	for _, b := range id {
		v = v<<8 | uint64(b)
	}
	return fmt.Sprintf("%016d", v)
}

// New は Manager を作る。providers は内蔵の経路を先に並べる。
func New(providers []Provider, st Store, log func(format string, args ...any), hooks Hooks) *Manager {
	if log == nil {
		log = func(string, ...any) {}
	}
	if st == nil {
		st = memStore{}
	}
	return &Manager{
		providers: providers, store: st, log: log, hooks: hooks,
		states: map[string]*readerState{}, ecm: map[string]ecmEntry{},
		emmQ: make(chan []byte, emmQueue), emmSeen: map[uint64]struct{}{},
	}
}

type memStore struct{}

func (memStore) Disabled() []string                       { return nil }
func (memStore) SetDisabled(n []string) ([]string, error) { return n, nil }

// Start は起動したときにカードを開いておき、切れていれば探し直す。
// 別の拠点から貰う設定のときは、手元のカードは開きません。
func (m *Manager) Start(ctx context.Context) {
	go func() {
		if m.remoteSource() == nil {
			m.opMu.Lock()
			m.ensureLocked()
			m.opMu.Unlock()
		}
		tk := time.NewTicker(5 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				m.opMu.Lock()
				m.dropLocked("止まります")
				m.opMu.Unlock()
				return
			case <-tk.C:
				m.opMu.Lock()
				m.mu.Lock()
				need := m.link == nil && m.remote == nil
				m.mu.Unlock()
				if need {
					m.ensureLocked()
				}
				m.opMu.Unlock()
			}
		}
	}()
}

func (m *Manager) disabledSet() map[string]bool {
	set := map[string]bool{}
	for _, n := range m.store.Disabled() {
		set[n] = true
	}
	return set
}

// dropLocked は開いているカードを手放す。opMu を持って呼ぶ。
func (m *Manager) dropLocked(why string) {
	m.mu.Lock()
	link, cur := m.link, m.cur
	m.link, m.haveInfo, m.b61Module = nil, false, 0
	m.ecm = map[string]ecmEntry{}
	m.mu.Unlock()
	if link != nil {
		link.Close()
		m.log("[%s] カードを手放しました (%s)", cur.Name, why)
	}
}

// fakeCardProvider は試験カードの Provider かどうかです。
// 試験カード (FAKE_CARD=1) は、登録の順に関わらず先に試し、先に並べます。
// 登録の順は init の順 (ファイル名の順) に頼っているので、Manager の側で決めを持ちます。
type fakeCardProvider interface {
	IsFakeCard() bool
}

func isFakeCardProvider(p Provider) bool {
	f, ok := p.(fakeCardProvider)
	return ok && f.IsFakeCard()
}

// fakeFirstProviders は Provider を、試験カードが先になるように並べます。
func (m *Manager) fakeFirstProviders() []Provider {
	var fake, rest []Provider
	for _, p := range m.providers {
		if isFakeCardProvider(p) {
			fake = append(fake, p)
		} else {
			rest = append(rest, p)
		}
	}
	return append(fake, rest...)
}

// ensureLocked はカードを開く。見つかったリーダーを内蔵 → 外付けの順に試し、
// カードが挿さっていて INT に応答した最初の 1 つで開く (9.1)。opMu を持って呼ぶ。
func (m *Manager) ensureLocked() error {
	m.mu.Lock()
	if m.link != nil {
		m.mu.Unlock()
		return nil
	}
	if time.Now().Before(m.failUntil) {
		msg := m.failMsg
		m.mu.Unlock()
		return errors.New(msg)
	}
	m.mu.Unlock()

	disabled := m.disabledSet()
	var internal, external []struct {
		p Provider
		r ReaderInfo
	}
	var listErr []string
	for _, p := range m.fakeFirstProviders() {
		rs, err := p.List()
		if err != nil {
			listErr = append(listErr, err.Error())
			continue
		}
		for _, r := range rs {
			e := struct {
				p Provider
				r ReaderInfo
			}{p, r}
			if r.Internal {
				internal = append(internal, e)
			} else {
				external = append(external, e)
			}
		}
	}
	states := map[string]*readerState{}
	for _, e := range append(internal, external...) {
		if disabled[e.r.Name] {
			continue
		}
		st := &readerState{}
		states[e.r.Name] = st
		link, err := e.p.Open(e.r.Name)
		if err != nil {
			st.err = err.Error()
			continue
		}
		info, ids, acas, err := probe(link)
		if err != nil {
			link.Close()
			st.err = err.Error()
			continue
		}
		m.mu.Lock()
		m.link, m.cur, m.info, m.idStrs, m.haveInfo, m.acas = link, e.r, info, ids, true, acas
		m.states = states
		m.failUntil, m.failMsg = time.Time{}, ""
		m.mu.Unlock()
		if acas {
			m.log("[%s] A-CAS カードを B-CAS 互換モードで開きました", e.r.Name)
		} else {
			m.log("[%s] カードを開きました", e.r.Name)
		}
		if m.hooks.Notify != nil {
			m.hooks.Notify()
		}
		return nil
	}

	msg := "使うカードリーダーがありません。カードリーダーをオンにしてください"
	if len(listErr) > 0 && len(internal)+len(external) == 0 {
		msg = listErr[0]
	}
	m.mu.Lock()
	m.states = states
	if m.failMsg != msg {
		m.log("%s", msg)
	}
	m.failUntil, m.failMsg = time.Now().Add(retryBackoff), msg
	m.mu.Unlock()
	return errors.New(msg)
}

func apdu(ins byte, body []byte) []byte { return apduFor(false, ins, body) }

// apduFor は B-CAS のコマンド。A-CAS カードの B-CAS 互換モードでは、同じコマンドの P2 だけを変える (acasP2)。
func apduFor(acas bool, ins byte, body []byte) []byte {
	a := []byte{0x90, ins, 0x00, 0x00}
	if acas {
		a[3] = acasP2(ins)
	}
	if len(body) > 0 {
		a = append(a, byte(len(body)))
		a = append(a, body...)
	}
	return append(a, 0x00)
}

// acasP2 は A-CAS カード (B-CAS 互換モード) の P2。INT と ECM は 2、IDI と EMM は 1、ほかは B-CAS と同じ 0。
// 実機の A-CAS カードで、B-CAS の INT (P2=0) は 6A86 で断られ、P2=2 の INT は 57 バイトを返すのを確かめた。
func acasP2(ins byte) byte {
	switch ins {
	case 0x30, 0x34:
		return 2
	case 0x32, 0x36:
		return 1
	}
	return 0
}

func code(resp []byte) uint16 { return binary.BigEndian.Uint16(resp[4:6]) }

// probe は INT と IDI を送る。B-CAS の INT が通らなければ、A-CAS の B-CAS 互換モードで INT を送り直す。
// A-CAS だったときは acas を true で返す。
func probe(l Link) (info b25.Info, ids []string, acas bool, err error) {
	resp, err := l.Transmit(apduFor(false, 0x30, nil))
	if err != nil {
		return b25.Info{}, nil, false, err
	}
	info, err = ParseINT(resp)
	if err != nil {
		// 2 バイトだけ (6A86 など) 返すのは、B-CAS の INT を受け付けない A-CAS カード
		resp2, err2 := l.Transmit(apduFor(true, 0x30, nil))
		if err2 != nil {
			return b25.Info{}, nil, false, fmt.Errorf("%v (A-CAS の INT も送れません: %v)", err, err2)
		}
		info2, err2 := ParseINT(resp2)
		if err2 != nil {
			return b25.Info{}, nil, false, fmt.Errorf("%v (A-CAS の INT も通りません: %v)", err, err2)
		}
		info, acas = info2, true
	}
	var strs []string
	if r, err := l.Transmit(apduFor(acas, 0x32, nil)); err == nil {
		info.IDs, strs = ParseIDI(r)
	}
	return info, strs, acas, nil
}

// ParseINT は INT の応答 (57 バイト以上。返り値 0x2100 でなければ B-CAS ではない)。
func ParseINT(resp []byte) (b25.Info, error) {
	var info b25.Info
	if len(resp) < 57 {
		return info, fmt.Errorf("INT の応答が短すぎます (%d バイト)", len(resp))
	}
	if c := code(resp); c != 0x2100 {
		return info, fmt.Errorf("B-CAS ではありません (INT の返り値 0x%04X)", c)
	}
	info.CASystemID = binary.BigEndian.Uint16(resp[6:8])
	copy(info.SystemKey[:], resp[16:48])
	copy(info.CBCInit[:], resp[48:56])
	return info, nil
}

// ParseIDI は IDI の応答。7 バイト目が番号の数で、その後ろに 10 バイトずつ (3〜8 バイト目の 48 ビットが番号)。
func ParseIDI(resp []byte) ([]b25.CardID, []string) {
	if len(resp) < 7 {
		return nil, nil
	}
	n := int(resp[6])
	var ids []b25.CardID
	var strs []string
	for i := 0; i < n && 7+10*(i+1) <= len(resp); i++ {
		e := resp[7+10*i : 7+10*(i+1)]
		var id b25.CardID
		copy(id[:], e[2:8])
		ids = append(ids, id)
		var v uint64
		for _, b := range id {
			v = v<<8 | uint64(b)
		}
		strs = append(strs, fmt.Sprintf("%016d", v))
	}
	return ids, strs
}

// send はカードを開いてから (A-CAS かどうかは開いたときに決まる) コマンドを送る。opMu を持って呼ぶ。
func (m *Manager) send(ins byte, body []byte, minLen int) ([]byte, error) {
	if err := m.ensureLocked(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	acas := m.acas
	m.mu.Unlock()
	return m.transmit(apduFor(acas, ins, body), minLen)
}

// Info は INT の結果。別の拠点から貰う設定のときは、配る側に問い合わせます。
func (m *Manager) Info() (b25.Info, error) {
	if r := m.remoteSource(); r != nil {
		return r.Init()
	}
	return m.localInfo()
}

// localInfo は手元のカードの INT の結果。開いていなければ開く。
func (m *Manager) localInfo() (b25.Info, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if err := m.ensureLocked(); err != nil {
		return b25.Info{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.info, nil
}

// transmit は 1 回送る。通信できなければ手放して、次の呼び出しで探し直す。opMu を持って呼ぶ。
func (m *Manager) transmit(a []byte, minLen int) ([]byte, error) {
	if err := m.ensureLocked(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	link := m.link
	m.mu.Unlock()
	resp, err := link.Transmit(a)
	if err != nil {
		m.dropLocked(err.Error())
		m.mu.Lock()
		m.failUntil, m.failMsg = time.Now().Add(retryBackoff), err.Error()
		m.mu.Unlock()
		return nil, err
	}
	if len(resp) < minLen {
		return nil, fmt.Errorf("応答が短すぎます (%d バイト)", len(resp))
	}
	return resp, nil
}

// ECM は ECM の中身に対する鍵。同じ中身には同じ鍵を返す (数秒だけ控える)。
// 別の拠点から貰う設定のときは、配る側に送ります。控えるのは手元と同じです。
func (m *Manager) ECM(ecm []byte) (b25.ECMResult, error) {
	if len(ecm) < 1 || len(ecm) > 255 {
		return b25.ECMResult{}, errors.New("ECM の長さは 1〜255 バイトです")
	}
	key := string(ecm)
	m.mu.Lock()
	if e, ok := m.ecm[key]; ok && time.Since(e.at) < ecmTTL {
		m.mu.Unlock()
		return e.res, nil
	}
	m.mu.Unlock()
	var res b25.ECMResult
	var err error
	if r := m.remoteSource(); r != nil {
		res, err = r.Ecm(ecm)
	} else {
		res, err = m.localECM(ecm)
	}
	if err != nil {
		return b25.ECMResult{}, err
	}
	m.mu.Lock()
	if len(m.ecm) > 64 {
		m.ecm = map[string]ecmEntry{}
	}
	m.ecm[key] = ecmEntry{res: res, at: time.Now()}
	m.mu.Unlock()
	return res, nil
}

// localECM は手元のカードに ECM を送って鍵を受け取ります。
func (m *Manager) localECM(ecm []byte) (b25.ECMResult, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	resp, err := m.send(0x34, ecm, 25)
	if err != nil {
		return b25.ECMResult{}, err
	}
	var res b25.ECMResult
	res.Code = code(resp)
	copy(res.Odd[:], resp[6:14])
	copy(res.Even[:], resp[14:22])
	return res, nil
}

// EMM は順番待ち (16 件) に入れて、別の goroutine が 1 秒に 1 件まで送る。止まらない。
// 配る側が古くて EMM を受け取れないときは、もう送りません。
func (m *Manager) EMM(payload []byte) {
	if m.remoteSource() != nil {
		m.mu.Lock()
		unsupported := m.emmUnsupported
		m.mu.Unlock()
		if unsupported {
			return
		}
	}
	m.emmOnce.Do(func() { go m.emmLoop() })
	h := fnv.New64a()
	h.Write(payload)
	m.mu.Lock()
	_, seen := m.emmSeen[h.Sum64()]
	m.mu.Unlock()
	if seen {
		return
	}
	select {
	case m.emmQ <- payload:
	default:
	}
}

// EMMNow は EMM をすぐにカードに送り、返り値を返す。配る側 (/emm) が使う。
// EMM (順番待ちに入れて 1 秒に 1 件まで送る) とは別で、受け入れの記録や denpa への通知はしない。
// 返すのはカードの返り値そのもので、0x2100 なら受け入れたことになる。
// 0x2100 のときは、更新前の応答を控えている ECM も消します (emmLoop と同じ)。
// denpa の付属のエージェントはこの返り値を見て、受け入れた EMM はもう送らず、
// 受け入れなかった EMM は次に流れてきたら送り直す (Emm.cs)。キューに入れただけでは 0x2100 を返さない。
func (m *Manager) EMMNow(payload []byte) (int, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	resp, err := m.send(0x36, payload, 6)
	if err != nil {
		return 0, err
	}
	c := int(code(resp))
	if c == 0x2100 {
		m.mu.Lock()
		m.ecm = map[string]ecmEntry{}
		m.mu.Unlock()
	}
	return c, nil
}

func (m *Manager) emmLoop() {
	for p := range m.emmQ {
		h := fnv.New64a()
		h.Write(p)
		sum := h.Sum64()
		m.mu.Lock()
		_, seen := m.emmSeen[sum]
		m.mu.Unlock()
		if seen {
			continue
		}
		var c uint16
		var sendErr error
		var unsupported bool
		if r := m.remoteSource(); r != nil {
			c, sendErr = r.Emm(p)
			unsupported = errors.Is(sendErr, ErrEmmUnsupported)
		} else {
			m.opMu.Lock()
			resp, err := m.send(0x36, p, 6)
			m.opMu.Unlock()
			if err == nil {
				c = code(resp)
			}
			sendErr = err
		}
		if unsupported {
			// 配る側が古くて EMM を受け取れません。もう送らず、この EMM は済んだことにします。
			// 鍵と契約確認はそのまま使います。
			m.mu.Lock()
			if len(m.emmSeen) >= emmRemember {
				m.emmSeen = map[uint64]struct{}{}
			}
			m.emmSeen[sum] = struct{}{}
			m.emmUnsupported = true
			m.mu.Unlock()
			time.Sleep(emmInterval)
			continue
		}
		if sendErr == nil {
			if c == 0x2100 || c == 0xA102 || c == 0xA107 {
				m.mu.Lock()
				if len(m.emmSeen) >= emmRemember {
					m.emmSeen = map[uint64]struct{}{}
				}
				m.emmSeen[sum] = struct{}{}
				m.mu.Unlock()
			}
			if c == 0x2100 {
				m.log("EMM を受け入れました")
				m.mu.Lock()
				m.ecm = map[string]ecmEntry{}
				notify := time.Since(m.lastNotify) >= notifyEvery
				if notify {
					m.lastNotify = time.Now()
				}
				m.mu.Unlock()
				if notify && m.hooks.Notify != nil {
					m.hooks.Notify()
				}
			}
		}
		time.Sleep(emmInterval)
	}
}

// Confirm は契約確認 (0x3C)。返り値だけ返す。
// 別の拠点から貰う設定のときは、配る側に渡します。
func (m *Manager) Confirm(ctx context.Context, verification []byte, mjd int) (int, error) {
	if r := m.remoteSource(); r != nil {
		code, err := r.Confirm(verification, uint16(mjd))
		return int(code), err
	}
	return m.localConfirm(ctx, verification, mjd)
}

// localConfirm は手元のカードに契約確認を送ります。
func (m *Manager) localConfirm(ctx context.Context, verification []byte, mjd int) (int, error) {
	body := append([]byte{byte(mjd >> 8), byte(mjd)}, verification...)
	m.opMu.Lock()
	defer m.opMu.Unlock()
	resp, err := m.send(0x3C, body, 6)
	if err != nil {
		return 0, err
	}
	return int(code(resp)), nil
}

// SetDisabled は使わないリーダーを書き、使っていれば手放して次のリーダーで繋ぎ直す。
func (m *Manager) SetDisabled(names []string) ([]string, error) {
	out, err := m.store.SetDisabled(names)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, n := range out {
		set[n] = true
	}
	m.opMu.Lock()
	m.mu.Lock()
	cur := m.cur.Name
	has := m.link != nil
	m.failUntil = time.Time{}
	m.mu.Unlock()
	if has && set[cur] {
		m.dropLocked("使わない設定にされました")
	}
	m.opMu.Unlock()
	if m.hooks.Notify != nil {
		m.hooks.Notify()
	}
	return out, nil
}

// Status は GET /denpa/card の本文 (9.6)。9 秒で切り上げて理由を返す。
func (m *Manager) Status(ctx context.Context) map[string]any {
	done := make(chan map[string]any, 1)
	go func() { done <- m.status() }()
	select {
	case r := <-done:
		return r
	case <-ctx.Done():
		out := map[string]any{"ok": false, "message": "カードが 10 秒以内に応答しません", "source": "local", "readers": []any{}}
		if r := m.remoteSource(); r != nil {
			out["source"] = "remote"
			out["remote"] = r.URL()
		}
		return out
	}
}

func (m *Manager) status() map[string]any {
	if r := m.remoteSource(); r != nil {
		return m.remoteStatus(r)
	}

	m.opMu.Lock()
	m.ensureLocked()
	m.opMu.Unlock()

	disabled := m.disabledSet()
	m.mu.Lock()
	cur, active, ids := m.cur, m.link != nil, append([]string{}, m.idStrs...)
	failMsg := m.failMsg
	states := m.states
	m.mu.Unlock()

	var tuners []string
	if m.hooks.Active != nil {
		tuners = m.hooks.Active()
	}
	if tuners == nil {
		tuners = []string{}
	}
	readers := []map[string]any{}
	var listMsg string
	seen := map[string]bool{}
	for _, p := range m.fakeFirstProviders() {
		rs, err := p.List()
		if err != nil {
			listMsg = err.Error()
			continue
		}
		for _, r := range rs {
			seen[r.Name] = true
			e := map[string]any{"name": r.Name, "card": nil, "ids": []string{}, "active": false, "tuners": []string{}, "disabled": disabled[r.Name]}
			if active && r.Name == cur.Name {
				e["card"], e["ids"], e["active"], e["tuners"] = "B-CAS", ids, true, tuners
			} else if st := states[r.Name]; st != nil && st.err != "" {
				e["error"] = st.err
			}
			readers = append(readers, e)
		}
	}
	// 使わない設定で、挿さっていないリーダーも残す (9.6)
	for n := range disabled {
		if !seen[n] {
			readers = append(readers, map[string]any{"name": n, "card": nil, "ids": []string{}, "active": false, "tuners": []string{}, "disabled": true})
		}
	}
	ok := active
	if m.hooks.Device != nil {
		for name, used := range m.hooks.Device() {
			label := name + " (機材の中で復号)"
			if disabled[label] {
				readers = append(readers, map[string]any{"name": label, "card": "B-CAS", "ids": []string{}, "active": false, "tuners": []string{}, "disabled": true})
				continue
			}
			if used == nil {
				used = []string{}
			}
			readers = append(readers, map[string]any{"name": label, "card": "B-CAS", "ids": []string{}, "active": true, "tuners": used, "disabled": false})
			ok = true
		}
	}
	msg := ""
	if !ok {
		msg = failMsg
		if msg == "" {
			msg = listMsg
		}
	}
	return map[string]any{"ok": ok, "message": msg, "source": "local", "readers": readers}
}

// remoteStatus は別の拠点から貰うときの GET /denpa/card の本文 (9.6)。
// 手元のリーダーは使わないので readers は空で、配る側のカード番号と URL を外に出します。
func (m *Manager) remoteStatus(r RemoteSource) map[string]any {
	tuners := []string{}
	if m.hooks.Active != nil {
		tuners = m.hooks.Active()
	}
	if tuners == nil {
		tuners = []string{}
	}
	// 問い合わせ直すので、配る側が落ちたり断ったりすればここで分かります。
	info, err := r.Check()
	if err != nil {
		return map[string]any{"ok": false, "message": err.Error(), "source": "remote", "remote": r.URL(), "ids": []string{}, "tuners": tuners, "readers": []map[string]any{}}
	}
	ids := make([]string, 0, len(info.IDs))
	for _, id := range info.IDs {
		ids = append(ids, cardIDString(id))
	}
	return map[string]any{"ok": true, "message": "", "source": "remote", "remote": r.URL(), "ids": ids, "tuners": tuners, "readers": []map[string]any{}}
}

// emmDiscard は DecodeFile で EMM を渡さないための包み。
type emmDiscard struct{ *Manager }

func (emmDiscard) EMM([]byte) {}

// DecodeStats は復号の数え。
type DecodeStats struct {
	Bytes     int64 // 出力した大きさ (ObserveTS では 0)
	Packets   int64 // 入力のパケット数
	Scrambled int64 // 入力でスクランブルされていたパケット (transport_scrambling_control が立っているもの)
	Remaining int64 // 出力にスクランブルのまま残ったパケット
}

// Decoded は復号できたパケットの数。
func (s DecodeStats) Decoded() int64 {
	if d := s.Scrambled - s.Remaining; d > 0 {
		return d
	}
	return 0
}

// Failed は復号できたとは言えないか。**復号できなかったのに成功を返さない。**
// カードが無い・ECM が流れていないときも、スクランブルされたまま全部書き出せてしまう。
// 残ったものが、復号できた数の 1% を超えたら失敗とする (付属のエージェントと同じ)。
func (s DecodeStats) Failed() bool { return s.Remaining*100 > s.Decoded() }

// DecodeStream は src の TS を復号して dst に書く。EMM は渡さない。数えを返す (成否は Failed で見る)。
func (m *Manager) DecodeStream(src io.Reader, dst io.Writer) (DecodeStats, error) {
	s := b25.NewStream(emmDiscard{m}, m.log)
	s.SetDecrypt(true)
	var before, after tscCounter // 入力でスクランブルされていた数と、出力に残った数
	var written int64
	write := func(b []byte) error {
		after.add(b)
		n, err := dst.Write(b)
		written += int64(n)
		return err
	}
	buf := make([]byte, 188*1024)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			before.add(buf[:n])
			if err := write(s.Process(buf[:n])); err != nil {
				return DecodeStats{}, err
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return DecodeStats{}, rerr
		}
	}
	if err := write(s.Flush()); err != nil {
		return DecodeStats{}, err
	}
	return DecodeStats{Bytes: written, Packets: before.total, Scrambled: before.n, Remaining: after.n}, nil
}

// ObserveTS は復号せずに、パケットとスクランブルされたパケットの数を数える。
func ObserveTS(src io.Reader) (DecodeStats, error) {
	var c tscCounter
	buf := make([]byte, 188*1024)
	for {
		n, err := src.Read(buf)
		c.add(buf[:n])
		if err == io.EOF {
			return DecodeStats{Packets: c.total, Scrambled: c.n}, nil
		}
		if err != nil {
			return DecodeStats{}, err
		}
	}
}

// DecodeFile はスクランブルされたまま録れた TS を復号して書く (9.7)。EMM は渡さない。
// 復号できなかったら失敗を返す (呼ぶ側が書きかけの出力を消す)。
func (m *Manager) DecodeFile(in, out string) (map[string]any, error) {
	src, err := os.Open(in)
	if err != nil {
		return nil, fmt.Errorf("入力を開けません: %v", err)
	}
	defer src.Close()
	dst, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, fmt.Errorf("書き出す先を作れません: %v", err)
	}
	defer dst.Close()
	stats, err := m.DecodeStream(src, dst)
	if err != nil {
		return nil, err
	}
	if stats.Failed() {
		return nil, fmt.Errorf("%d パケットがスクランブルのままです (%s)", stats.Remaining, m.DecodeFailReason())
	}
	return map[string]any{"packets": stats.Packets, "scrambled": stats.Remaining}, nil
}

// DecodeFailReason は復号できなかった理由。カードを開けなかった理由があればそれ、無ければ一般的な言い方。
func (m *Manager) DecodeFailReason() string {
	m.mu.Lock()
	msg := m.failMsg
	m.mu.Unlock()
	if msg != "" {
		return msg
	}
	return "鍵を受け取れませんでした (カードを使えない・ECM が流れていないなど)"
}

// tscCounter は TS の流れの中で、transport_scrambling_control が立っているパケットを数える。
// パケットの境目の探し方は b25.Stream.Process と同じ (先頭の余分なバイトを飛ばし、188 バイト先も 0x47 のものを境目とする)。
// 入力の数え方が復号側とずれると、先頭にごみのある録画で、復号できた数が 0 になってしまう。
type tscCounter struct {
	buf   []byte // 前の塊の端数 (188 バイトに満たない分)
	n     int64  // スクランブルされていたパケット
	total int64  // 数えたパケット
}

func (c *tscCounter) add(b []byte) {
	c.buf = append(c.buf, b...)
	pos := 0
	for len(c.buf)-pos >= 188 {
		if c.buf[pos] != 0x47 || (len(c.buf)-pos > 188 && c.buf[pos+188] != 0x47) {
			i := bytes.IndexByte(c.buf[pos+1:], 0x47)
			if i < 0 {
				pos = len(c.buf)
				break
			}
			pos += 1 + i
			continue
		}
		c.total++
		if c.buf[pos+3]&0xC0 != 0 {
			c.n++
		}
		pos += 188
	}
	c.buf = append(c.buf[:0], c.buf[pos:]...)
}

// NewTLVStream は 4K (MMT/TLV) の選局ごとの復号を作る (docs/b61.md)。
func (m *Manager) NewTLVStream(tuner string) *b61.Stream {
	return b61.NewStream(m, func(format string, args ...any) {
		m.log("[%s] %s", tuner, fmt.Sprintf(format, args...))
	})
}

// NewStream は選局ごとの復号を作る。logPrefix はチューナーの名前。
func (m *Manager) NewStream(tuner string) *b25.Stream {
	s := b25.NewStream(m, func(format string, args ...any) {
		m.log("[%s] %s", tuner, fmt.Sprintf(format, args...))
	})
	s.Debug = os.Getenv("B25_DEBUG") == "1"
	return s
}

// ReaderReport は Probe の、リーダー 1 つぶんの結果。
type ReaderReport struct {
	Name       string
	Internal   bool
	Err        string   // 開けない・INT が通らない理由 (空なら読めた)
	Card       string   // "B-CAS" か "A-CAS" (Err が空のとき)
	CASystemID uint16   // INT の CA_system_id
	IDs        []string // IDI のカード ID (16 桁。カードの個人情報なので、記録に残さないこと)
}

// Probe は見つかったリーダーを順に (内蔵 → 外付け) 開いて、INT と IDI を読み、閉じる (`--card`)。
// 使わない設定 (`cards.json`) のリーダーも、確かめるために開く。排他で開くので、サーバが動いていると
// 共有違反で開けない (理由が Err に入る)。リーダーを 1 つも見つけられなければ、理由を error で返す。
func (m *Manager) Probe() ([]ReaderReport, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	var internal, external []ReaderReport
	var errs []string
	for _, p := range m.fakeFirstProviders() {
		rs, err := p.List()
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		for _, r := range rs {
			rep := ReaderReport{Name: r.Name, Internal: r.Internal}
			if link, err := p.Open(r.Name); err != nil {
				rep.Err = err.Error()
			} else {
				info, ids, acas, err := probe(link)
				link.Close()
				if err != nil {
					rep.Err = err.Error()
				} else {
					rep.Card, rep.CASystemID, rep.IDs = "B-CAS", info.CASystemID, ids
					if acas {
						rep.Card = "A-CAS"
					}
				}
			}
			if r.Internal {
				internal = append(internal, rep)
			} else {
				external = append(external, rep)
			}
		}
	}
	out := append(internal, external...)
	if len(out) == 0 {
		if len(errs) > 0 {
			return nil, errors.New(strings.Join(errs, "; "))
		}
		return nil, errors.New("使うカードリーダーがありません。カードリーダーをオンにしてください")
	}
	return out, nil
}
