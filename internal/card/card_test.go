package card

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeLink は B-CAS のふりをする。
type fakeLink struct {
	mu     sync.Mutex
	sent   [][]byte
	broken bool
	emmRet uint16
}

func resp(code uint16, n int) []byte {
	r := make([]byte, n)
	r[4], r[5] = byte(code>>8), byte(code)
	return r
}

func (l *fakeLink) Transmit(a []byte) ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sent = append(l.sent, append([]byte(nil), a...))
	if l.broken {
		return nil, errors.New("リーダーが切れました")
	}
	switch a[1] {
	case 0x30:
		r := resp(0x2100, 57)
		r[6], r[7] = 0x00, 0x05
		for i := 16; i < 48; i++ {
			r[i] = byte(i)
		}
		for i := 48; i < 56; i++ {
			r[i] = byte(i)
		}
		return r, nil
	case 0x32:
		r := resp(0x2100, 7+10*2)
		r[6] = 2
		copy(r[7:], []byte{0, 0, 0x00, 0x00, 0x00, 0x00, 0x30, 0x39, 0, 0}) // 12345
		copy(r[17:], []byte{0, 0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0, 0})
		return r, nil
	case 0x34:
		r := resp(0x0800, 25)
		for i := 6; i < 14; i++ {
			r[i] = 0xAA
		}
		for i := 14; i < 22; i++ {
			r[i] = 0xBB
		}
		return r, nil
	case 0x36:
		return resp(l.emmRet, 8), nil
	case 0x3C:
		return resp(0x0800, 8), nil
	}
	return resp(0xFFFF, 8), nil
}
func (l *fakeLink) Close() error { return nil }

func (l *fakeLink) count(ins byte) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, a := range l.sent {
		if a[1] == ins {
			n++
		}
	}
	return n
}

// sentCopy は送った APDU の写しです。EMM の goroutine が送っている最中にも読めます。
func (l *fakeLink) sentCopy() [][]byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([][]byte, len(l.sent))
	copy(out, l.sent)
	return out
}

type fakeProvider struct {
	readers []ReaderInfo
	err     error
	links   map[string]*fakeLink
	openErr map[string]error
	isFake  bool
}

func (p *fakeProvider) List() ([]ReaderInfo, error) { return p.readers, p.err }

// IsFakeCard は試験カードのふりをするかどうかです。Manager の fake-first の決めのテスト用です。
func (p *fakeProvider) IsFakeCard() bool { return p.isFake }
func (p *fakeProvider) Open(name string) (Link, error) {
	if e := p.openErr[name]; e != nil {
		return nil, e
	}
	if l := p.links[name]; l != nil {
		return l, nil
	}
	return nil, errors.New("カードがありません")
}

type memDisabled struct{ names []string }

func (s *memDisabled) Disabled() []string { return s.names }
func (s *memDisabled) SetDisabled(n []string) ([]string, error) {
	s.names = n
	return n, nil
}

// acasLink は A-CAS カード (B-CAS 互換モード) のふり。P2 が合わないコマンドは 6A86 の 2 バイトで断る。
type acasLink struct{ fakeLink }

func (l *acasLink) Transmit(a []byte) ([]byte, error) {
	if a[3] != acasP2(a[1]) {
		l.mu.Lock()
		l.sent = append(l.sent, append([]byte(nil), a...))
		l.mu.Unlock()
		return []byte{0x6A, 0x86}, nil
	}
	return l.fakeLink.Transmit(a)
}

type acasProvider struct{ l *acasLink }

func (p acasProvider) List() ([]ReaderInfo, error) { return []ReaderInfo{{Name: "X"}}, nil }
func (p acasProvider) Open(string) (Link, error)   { return p.l, nil }

func TestACAS_B_CASのINTが断られたらP2を変えて開く(t *testing.T) {
	l := &acasLink{}
	m := New([]Provider{acasProvider{l}}, nil, nil, Hooks{})
	info, err := m.Info()
	if err != nil || info.CASystemID != 5 {
		t.Fatalf("%+v %v", info, err)
	}
	if len(info.IDs) != 2 {
		t.Errorf("IDI を P2=1 で読めていない: %v", info.IDs)
	}
	// 1 つ目は B-CAS の INT (P2=0) で断られ、次に P2=2 で通る
	if l.sent[0][3] != 0 || l.sent[1][3] != 2 || l.sent[1][1] != 0x30 {
		t.Errorf("INT の送り方: % x / % x", l.sent[0], l.sent[1])
	}
	if _, err := m.ECM([]byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	m.EMM([]byte{9, 9})
	time.Sleep(200 * time.Millisecond)
	for _, a := range l.sentCopy() {
		if a[1] == 0x34 && a[3] != 2 {
			t.Errorf("ECM の P2 は 2: % x", a)
		}
		if a[1] == 0x36 && a[3] != 1 {
			t.Errorf("EMM の P2 は 1: % x", a)
		}
		if a[1] == 0x32 && a[3] != 1 {
			t.Errorf("IDI の P2 は 1: % x", a)
		}
	}
	if l.count(0x34) != 1 {
		t.Errorf("ECM が送られていない")
	}
}

func TestACAS_B_CASのカードはP2を変えない(t *testing.T) {
	l := &fakeLink{}
	m := New([]Provider{&fakeProvider{readers: []ReaderInfo{{Name: "A", Internal: true}}, links: map[string]*fakeLink{"A": l}}}, nil, nil, Hooks{})
	if _, err := m.ECM([]byte{1}); err != nil {
		t.Fatal(err)
	}
	for _, a := range l.sent {
		if a[3] != 0 {
			t.Errorf("B-CAS なのに P2 が 0 でない: % x", a)
		}
	}
}

// TestEMMNow_受け入れたらECMの控えを消す は、EMM を受け入れた (0x2100) あと、
// 更新前の応答を控えている ECM が消えることを確かめます。
func TestEMMNow_受け入れたらECMの控えを消す(t *testing.T) {
	l := &fakeLink{emmRet: 0x2100}
	m := New([]Provider{&fakeProvider{readers: []ReaderInfo{{Name: "A", Internal: true}}, links: map[string]*fakeLink{"A": l}}}, nil, nil, Hooks{})
	ecm := []byte{1, 2, 3}
	if _, err := m.ECM(ecm); err != nil {
		t.Fatal(err)
	}
	if n := l.count(0x34); n != 1 {
		t.Fatalf("ECM が %d 回送られました", n)
	}
	if code, err := m.EMMNow([]byte{9, 9}); err != nil || code != 0x2100 {
		t.Fatalf("EMMNow: %d %v", code, err)
	}
	if _, err := m.ECM(ecm); err != nil {
		t.Fatal(err)
	}
	if n := l.count(0x34); n != 2 {
		t.Fatalf("受け入れたあとの ECM が控えのままです (%d 回)", n)
	}
}

func TestACAS_どちらでもなければ元の理由(t *testing.T) {
	l := &acasLink{}
	l.broken = true // 通信できない
	m := New([]Provider{acasProvider{l}}, nil, nil, Hooks{})
	if _, err := m.Info(); err == nil {
		t.Error("開けてしまった")
	}
}

func TestParseINT_IDI(t *testing.T) {
	info, err := ParseINT((&fakeLink{}).mustINT())
	if err != nil || info.CASystemID != 5 || info.SystemKey[0] != 16 || info.SystemKey[31] != 47 || info.CBCInit[0] != 48 || info.CBCInit[7] != 55 {
		t.Fatalf("%+v %v", info, err)
	}
	if _, err := ParseINT(resp(0x2100, 56)); err == nil {
		t.Error("57 バイト未満は断る")
	}
	if _, err := ParseINT(resp(0x1234, 57)); err == nil {
		t.Error("返り値が 0x2100 でなければ B-CAS ではない")
	}
	r, _ := (&fakeLink{}).Transmit(apdu(0x32, nil))
	ids, strs := ParseIDI(r)
	if len(ids) != 2 || strs[0] != "0000000000012345" || strs[1] != "0000000000000001" {
		t.Errorf("番号は 10 進 16 桁: %v", strs)
	}
}

func (l *fakeLink) mustINT() []byte { r, _ := l.Transmit(apdu(0x30, nil)); return r }

func TestEnsure_内蔵を先に_カードの無いリーダーは飛ばす(t *testing.T) {
	ext := &fakeLink{}
	in := &fakeLink{}
	p := &fakeProvider{
		readers: []ReaderInfo{{Name: "外付け", Internal: false}, {Name: "空の内蔵", Internal: true}, {Name: "内蔵", Internal: true}},
		links:   map[string]*fakeLink{"外付け": ext, "内蔵": in},
	}
	m := New([]Provider{p}, &memDisabled{}, nil, Hooks{})
	if _, err := m.Info(); err != nil {
		t.Fatal(err)
	}
	if m.cur.Name != "内蔵" {
		t.Errorf("使うリーダー: %s", m.cur.Name)
	}
	if ext.count(0x30) != 0 {
		t.Errorf("先に見つかった内蔵で開けたら外付けには触らない")
	}
}

// TestEnsure_試験カードは登録の順に関わらず先に試す は、
// FAKE_CARD=1 の試験カードが、登録の順 (init の順) に関わらず先に試され、
// /denpa/card でも先に並ぶことを確かめます。
func TestEnsure_試験カードは登録の順に関わらず先に試す(t *testing.T) {
	real := &fakeLink{}
	fake := &fakeLink{}
	m := New([]Provider{
		&fakeProvider{readers: []ReaderInfo{{Name: "A", Internal: true}}, links: map[string]*fakeLink{"A": real}},
		&fakeProvider{isFake: true, readers: []ReaderInfo{{Name: "fake", Internal: true}}, links: map[string]*fakeLink{"fake": fake}},
	}, nil, nil, Hooks{})
	if _, err := m.Info(); err != nil {
		t.Fatal(err)
	}
	if m.cur.Name != "fake" {
		t.Fatalf("試験カードが先に選ばれません: %s", m.cur.Name)
	}
	if fake.count(0x30) != 1 || real.count(0x30) != 0 {
		t.Fatalf("INT の送り先が違います (fake %d, A %d)", fake.count(0x30), real.count(0x30))
	}
	st := m.Status(context.Background())
	readers, _ := st["readers"].([]map[string]any)
	if len(readers) == 0 || readers[0]["name"] != "fake" {
		t.Fatalf("/denpa/card で fake が先頭に並びません: %+v", st)
	}
}

// TestEnsure_試験カードでなければ登録の順のまま は、
// IsFakeCard が false の Provider では、登録の順が変わらないことを確かめます。
func TestEnsure_試験カードでなければ登録の順のまま(t *testing.T) {
	a := &fakeLink{}
	b := &fakeLink{}
	m := New([]Provider{
		&fakeProvider{readers: []ReaderInfo{{Name: "A", Internal: true}}, links: map[string]*fakeLink{"A": a}},
		&fakeProvider{readers: []ReaderInfo{{Name: "B", Internal: true}}, links: map[string]*fakeLink{"B": b}},
	}, nil, nil, Hooks{})
	if _, err := m.Info(); err != nil {
		t.Fatal(err)
	}
	if m.cur.Name != "A" {
		t.Fatalf("先に登録したリーダーが選ばれません: %s", m.cur.Name)
	}
}

func TestEnsure_使わない設定のリーダーは飛ばす(t *testing.T) {
	p := &fakeProvider{
		readers: []ReaderInfo{{Name: "A", Internal: true}, {Name: "B"}},
		links:   map[string]*fakeLink{"A": {}, "B": {}},
	}
	m := New([]Provider{p}, &memDisabled{names: []string{"A"}}, nil, Hooks{})
	m.Info()
	if m.cur.Name != "B" {
		t.Errorf("A は使わない: %s", m.cur.Name)
	}
}

func TestEnsure_開けなければ理由を返し10秒は探し直さない(t *testing.T) {
	p := &fakeProvider{readers: []ReaderInfo{{Name: "A", Internal: true}}, openErr: map[string]error{"A": errors.New("共有違反")}}
	m := New([]Provider{p}, &memDisabled{}, nil, Hooks{})
	if _, err := m.Info(); err == nil {
		t.Fatal("開けないはず")
	}
	st := m.status()
	rs := st["readers"].([]map[string]any)
	if st["ok"] != false || rs[0]["error"] != "共有違反" {
		t.Errorf("status: %+v", st)
	}
	p.openErr = nil
	p.links = map[string]*fakeLink{"A": {}}
	if _, err := m.Info(); err == nil {
		t.Error("10 秒は探し直さず、同じ理由で断る")
	}
}

func TestEnsure_経路に繋がらなければその理由(t *testing.T) {
	p := &fakeProvider{err: errors.New("pcscd に繋がりません (/run/pcscd/pcscd.comm)")}
	m := New([]Provider{p}, &memDisabled{}, nil, Hooks{})
	st := m.Status(context.Background())
	if st["ok"] != false || st["message"] != "pcscd に繋がりません (/run/pcscd/pcscd.comm)" {
		t.Errorf("%+v", st)
	}
}

func TestECM_同じ中身は控えた答えを返す(t *testing.T) {
	l := &fakeLink{}
	m := New([]Provider{&fakeProvider{readers: []ReaderInfo{{Name: "A", Internal: true}}, links: map[string]*fakeLink{"A": l}}}, nil, nil, Hooks{})
	r1, err := m.ECM([]byte{1, 2, 3})
	if err != nil || r1.Code != 0x0800 || r1.Odd[0] != 0xAA || r1.Even[0] != 0xBB {
		t.Fatalf("%+v %v", r1, err)
	}
	m.ECM([]byte{1, 2, 3})
	m.ECM([]byte{4})
	if l.count(0x34) != 2 {
		t.Errorf("同じ ECM は 1 回だけ: %d", l.count(0x34))
	}
	// APDU の形: 90 34 00 00 <Lc> <ECM> 00
	l.mu.Lock()
	var got []byte
	for _, a := range l.sent {
		if a[1] == 0x34 {
			got = a
			break
		}
	}
	l.mu.Unlock()
	if !bytes.Equal(got, []byte{0x90, 0x34, 0, 0, 3, 1, 2, 3, 0}) {
		t.Errorf("APDU: % x", got)
	}
	if _, err := m.ECM(nil); err == nil {
		t.Error("空の ECM は断る")
	}
}

func TestTransmit_通信できなければ手放して探し直す(t *testing.T) {
	l1, l2 := &fakeLink{}, &fakeLink{}
	p := &fakeProvider{readers: []ReaderInfo{{Name: "A", Internal: true}}, links: map[string]*fakeLink{"A": l1}}
	m := New([]Provider{p}, nil, nil, Hooks{})
	if _, err := m.ECM([]byte{1}); err != nil {
		t.Fatal(err)
	}
	l1.broken = true
	if _, err := m.ECM([]byte{2}); err == nil {
		t.Fatal("切れたら失敗")
	}
	p.links["A"] = l2
	m.mu.Lock()
	m.failUntil = time.Time{}
	m.mu.Unlock()
	if _, err := m.ECM([]byte{3}); err != nil {
		t.Errorf("探し直して INT からやり直す: %v", err)
	}
	if l2.count(0x30) != 1 {
		t.Errorf("INT からやり直す")
	}
}

func TestEMM_受け入れたら控えてもう送らない(t *testing.T) {
	l := &fakeLink{emmRet: 0x2100}
	notified := make(chan struct{}, 4)
	m := New([]Provider{&fakeProvider{readers: []ReaderInfo{{Name: "A", Internal: true}}, links: map[string]*fakeLink{"A": l}}}, nil, nil,
		Hooks{Notify: func() { notified <- struct{}{} }})
	m.Info()
	<-notified // 開いたときの通知
	payload := append([]byte{1, 2, 3, 4, 5, 6, 6}, make([]byte, 6)...)
	m.EMM(payload)
	deadline := time.Now().Add(3 * time.Second)
	for l.count(0x36) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if l.count(0x36) != 1 {
		t.Fatalf("EMM が送られない")
	}
	select {
	case <-notified:
	case <-time.After(2 * time.Second):
		t.Error("受け入れたら card の通知")
	}
	m.EMM(payload)
	time.Sleep(1300 * time.Millisecond)
	if l.count(0x36) != 1 {
		t.Errorf("応答したものはもう送らない: %d", l.count(0x36))
	}
}

func TestConfirm(t *testing.T) {
	l := &fakeLink{}
	m := New([]Provider{&fakeProvider{readers: []ReaderInfo{{Name: "A", Internal: true}}, links: map[string]*fakeLink{"A": l}}}, nil, nil, Hooks{})
	code, err := m.Confirm(context.Background(), []byte{9, 8, 7}, 0x1234)
	if err != nil || code != 0x0800 {
		t.Fatalf("%d %v", code, err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	last := l.sent[len(l.sent)-1]
	if !bytes.Equal(last, []byte{0x90, 0x3C, 0, 0, 5, 0x12, 0x34, 9, 8, 7, 0}) {
		t.Errorf("APDU: % x", last)
	}
}

func TestStatus_リーダーの表と使わない設定と機材の中で復号するもの(t *testing.T) {
	l := &fakeLink{}
	st := &memDisabled{names: []string{"抜いたリーダー"}}
	m := New([]Provider{&fakeProvider{
		readers: []ReaderInfo{{Name: "内蔵", Internal: true}}, links: map[string]*fakeLink{"内蔵": l},
	}}, st, nil, Hooks{
		Active: func() []string { return []string{"PX #0"} },
		Device: func() map[string][]string { return map[string][]string{"nasne": nil} },
	})
	res := m.Status(context.Background())
	if res["ok"] != true || res["source"] != "local" || res["message"] != "" {
		t.Fatalf("%+v", res)
	}
	rs := res["readers"].([]map[string]any)
	if len(rs) != 3 {
		t.Fatalf("readers: %+v", rs)
	}
	if rs[0]["name"] != "内蔵" || rs[0]["card"] != "B-CAS" || rs[0]["active"] != true {
		t.Errorf("内蔵: %+v", rs[0])
	}
	if ids := rs[0]["ids"].([]string); len(ids) != 2 || ids[0] != "0000000000012345" {
		t.Errorf("ids: %v", ids)
	}
	var sawGone, sawDevice bool
	for _, r := range rs[1:] {
		if r["name"] == "抜いたリーダー" && r["disabled"] == true {
			sawGone = true
		}
		if r["name"] == "nasne (機材の中で復号)" && r["card"] == "B-CAS" && r["active"] == true {
			sawDevice = true
		}
	}
	if !sawGone || !sawDevice {
		t.Errorf("挿さっていない使わない設定は残る / 機材の中で復号する擬似のリーダー: %+v", rs)
	}
}

func TestSetDisabled_使っていれば手放す(t *testing.T) {
	a, b := &fakeLink{}, &fakeLink{}
	m := New([]Provider{&fakeProvider{
		readers: []ReaderInfo{{Name: "A", Internal: true}, {Name: "B"}},
		links:   map[string]*fakeLink{"A": a, "B": b},
	}}, &memDisabled{}, nil, Hooks{})
	m.Info()
	if m.cur.Name != "A" {
		t.Fatal(m.cur.Name)
	}
	if _, err := m.SetDisabled([]string{"A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Info(); err != nil || m.cur.Name != "B" {
		t.Errorf("次のリーダーで繋ぎ直す: %v %s", err, m.cur.Name)
	}
}

func TestDecodeFile(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.ts")
	pkt := bytes.Repeat([]byte{0x47, 0x1F, 0xFF, 0x10}, 47) // ヌルパケット (PID 0x1FFF)
	os.WriteFile(in, bytes.Repeat(pkt, 4), 0o644)
	m := New([]Provider{&fakeProvider{readers: []ReaderInfo{{Name: "A", Internal: true}}, links: map[string]*fakeLink{"A": {}}}}, nil, nil, Hooks{})
	out := filepath.Join(dir, "out.ts")
	res, err := m.DecodeFile(in, out)
	if err != nil {
		t.Fatal(err)
	}
	if res["packets"] != int64(4) {
		t.Errorf("%+v", res)
	}
	b, _ := os.ReadFile(out)
	if len(b) != 188*4 {
		t.Errorf("書き出した大きさ: %d", len(b))
	}
	if _, err := m.DecodeFile(in, out); err == nil {
		t.Error("書き出す先がもうあれば断る")
	}
}
