package b25

import (
	"bytes"
	"errors"
	"sync"
	"testing"
	"time"
)

var (
	testSys = func() (k [32]byte) {
		for i := range k {
			k[i] = byte(i*7 + 3)
		}
		return
	}()
	testCBC = [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	oddKey  = [8]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}
	evenKey = [8]byte{0xA1, 0xB2, 0xC3, 0xD4, 0xE5, 0xF6, 0x07, 0x18}
)

func TestMulti2_暗号化と復号が戻る(t *testing.T) {
	m := NewMulti2(testSys, testCBC)
	m.SetScrambleKey(oddKey)
	for _, n := range []int{0, 1, 7, 8, 9, 16, 173, 176, 184} {
		plain := make([]byte, n)
		for i := range plain {
			plain[i] = byte(i * 13)
		}
		data := append([]byte(nil), plain...)
		m.Encrypt(data)
		if n >= 8 && bytes.Equal(data, plain) {
			t.Errorf("n=%d: 暗号化されていない", n)
		}
		m.Decrypt(data)
		if !bytes.Equal(data, plain) {
			t.Errorf("n=%d: 戻らない", n)
		}
	}
}

func TestMulti2_鍵が違えば戻らない(t *testing.T) {
	a := NewMulti2(testSys, testCBC)
	a.SetScrambleKey(oddKey)
	b := NewMulti2(testSys, testCBC)
	b.SetScrambleKey(evenKey)
	plain := bytes.Repeat([]byte{0, 0, 1, 0xE0}, 46)
	data := append([]byte(nil), plain...)
	a.Encrypt(data)
	b.Decrypt(data)
	if bytes.Equal(data, plain) {
		t.Error("違う鍵で戻ってしまった")
	}
}

// ---- TS を作る道具 ----

func section(tableID byte, ext uint16, body []byte) []byte {
	n := 5 + len(body) + 4
	sec := []byte{tableID, 0xB0 | byte(n>>8), byte(n), byte(ext >> 8), byte(ext), 0xC1, 0, 0}
	sec = append(sec, body...)
	crc := crc32mpeg(sec)
	return append(sec, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

var ccs = map[uint16]byte{}

func tsPacket(pid uint16, pusi bool, scr byte, payload []byte) []byte {
	p := make([]byte, 188)
	for i := range p {
		p[i] = 0xFF
	}
	p[0] = 0x47
	p[1] = byte(pid >> 8)
	if pusi {
		p[1] |= 0x40
	}
	p[2] = byte(pid)
	p[3] = scr<<6 | 0x10 | ccs[pid]&0x0F
	ccs[pid]++
	copy(p[4:], payload)
	return p
}

func psiPacket(pid uint16, sec []byte) []byte {
	return tsPacket(pid, true, 0, append([]byte{0}, sec...))
}

func patPacket(pmtPID uint16) []byte {
	return psiPacket(0, section(0x00, 1, []byte{0, 1, 0xE0 | byte(pmtPID>>8), byte(pmtPID)}))
}

// pmtPacket は映像 (0x1B) 1 つの PMT。ECM の PID を ES の CA 記述子で示す。
func pmtPacket(pmtPID, esPID, ecmPID uint16, withCA bool) []byte {
	body := []byte{0xE1, 0x01, 0xF0, 0x00} // PCR PID, program_info_length=0
	body = append(body, 0x1B, 0xE0|byte(esPID>>8), byte(esPID))
	if withCA {
		d := []byte{0x09, 0x04, 0x00, 0x05, 0xE0 | byte(ecmPID>>8), byte(ecmPID)}
		body = append(body, 0xF0, byte(len(d)))
		body = append(body, d...)
	} else {
		body = append(body, 0xF0, 0x00)
	}
	return psiPacket(pmtPID, section(0x02, 1, body))
}

func ecmPacket(ecmPID uint16, content []byte) []byte {
	return psiPacket(ecmPID, section(0x82, 0, content))
}

// 空の 8 バイトずつ (23 ブロック) の PES 先頭を持つペイロード。
func pesPayload(seed byte) []byte {
	p := make([]byte, 184)
	for i := range p {
		p[i] = byte(i) ^ seed
	}
	copy(p, []byte{0, 0, 1, 0xE0})
	return p
}

func scrambledES(pid uint16, key [8]byte, odd bool, plain []byte) []byte {
	m := NewMulti2(testSys, testCBC)
	m.SetScrambleKey(key)
	enc := append([]byte(nil), plain...)
	m.Encrypt(enc)
	scr := byte(2)
	if odd {
		scr = 3
	}
	p := tsPacket(pid, true, scr, enc)
	return p
}

type fakeCard struct {
	mu    sync.Mutex
	calls [][]byte
	code  uint16
	err   error
	emms  [][]byte
	ids   []CardID
}

func (c *fakeCard) Info() (Info, error) {
	return Info{SystemKey: testSys, CBCInit: testCBC, CASystemID: 5, IDs: c.ids}, nil
}
func (c *fakeCard) ECM(ecm []byte) (ECMResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, append([]byte(nil), ecm...))
	if c.err != nil {
		return ECMResult{}, c.err
	}
	return ECMResult{Odd: oddKey, Even: evenKey, Code: c.code}, nil
}
func (c *fakeCard) EMM(p []byte) {
	c.mu.Lock()
	c.emms = append(c.emms, p)
	c.mu.Unlock()
}

func concat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func packets(b []byte) [][]byte {
	var out [][]byte
	for len(b) >= 188 {
		out = append(out, b[:188])
		b = b[188:]
	}
	return out
}

func TestStream_ECMの鍵でESを復号する(t *testing.T) {
	card := &fakeCard{code: 0x0800}
	s := NewStream(card, t.Logf)
	s.SetDecrypt(true)
	plainOdd, plainEven := pesPayload(1), pesPayload(2)
	in := concat(
		patPacket(0x100), pmtPacket(0x100, 0x101, 0x102, true),
		ecmPacket(0x102, []byte{1, 2, 3, 4, 5}),
		scrambledES(0x101, oddKey, true, plainOdd),
		scrambledES(0x101, evenKey, false, plainEven),
	)
	out := packets(s.Process(in))
	if len(out) != 5 {
		t.Fatalf("5 パケットのはず: %d", len(out))
	}
	if !bytes.Equal(out[3][4:], plainOdd) || out[3][3]>>6 != 0 {
		t.Errorf("奇数鍵で戻らない (tsc=%d)", out[3][3]>>6)
	}
	if !bytes.Equal(out[4][4:], plainEven) || out[4][3]>>6 != 0 {
		t.Errorf("偶数鍵で戻らない")
	}
	if len(card.calls) != 1 || !bytes.Equal(card.calls[0], []byte{1, 2, 3, 4, 5}) {
		t.Errorf("ECM の中身は先頭 8 バイトと CRC を除いた部分: %v", card.calls)
	}
	if _, u := s.Stats(); u != 0 {
		t.Errorf("復号できなかった数: %d", u)
	}
}

func TestStream_同じECMはカードに問い合わせ直さない_変われば問い合わせる(t *testing.T) {
	card := &fakeCard{code: 0x0800}
	s := NewStream(card, t.Logf)
	s.SetDecrypt(true)
	s.Process(concat(patPacket(0x100), pmtPacket(0x100, 0x101, 0x102, true),
		ecmPacket(0x102, []byte{1, 2, 3}), ecmPacket(0x102, []byte{1, 2, 3}), ecmPacket(0x102, []byte{9, 9, 9})))
	if len(card.calls) != 2 {
		t.Errorf("問い合わせは 2 回: %d", len(card.calls))
	}
}

func TestStream_鍵が来るまで溜める(t *testing.T) {
	card := &fakeCard{code: 0x0800}
	s := NewStream(card, t.Logf)
	s.SetDecrypt(true)
	out := s.Process(concat(patPacket(0x100), pmtPacket(0x100, 0x101, 0x102, true), scrambledES(0x101, oddKey, true, pesPayload(1))))
	if len(out) != 0 {
		t.Errorf("ECM の答えがそろうまで出さない: %d バイト", len(out))
	}
	out = s.Process(ecmPacket(0x102, []byte{7, 7}))
	ps := packets(out)
	if len(ps) != 4 || !bytes.Equal(ps[2][4:], pesPayload(1)) {
		t.Fatalf("そろったら先頭から復号して出す: %d", len(ps))
	}
}

// pmtWithNullCA は ES を 2 つ持つ PMT。1 つ目は ecmPID の ECM で復号する映像 (0x1B)、2 つ目 (0x0D) は CA 記述子の PID が
// 0x1FFF (ARIB で「ECM なし = スクランブルされていない ES」の印)。
func pmtWithNullCA(pmtPID, esPID, ecmPID, freeESPID uint16) []byte {
	body := []byte{0xE1, 0x01, 0xF0, 0x00}
	d := []byte{0x09, 0x04, 0x00, 0x05, 0xE0 | byte(ecmPID>>8), byte(ecmPID)}
	body = append(body, 0x1B, 0xE0|byte(esPID>>8), byte(esPID), 0xF0, byte(len(d)))
	body = append(body, d...)
	nul := []byte{0x09, 0x04, 0x00, 0x05, 0xFF, 0xFF}
	body = append(body, 0x0D, 0xE0|byte(freeESPID>>8), byte(freeESPID), 0xF0, byte(len(nul)))
	body = append(body, nul...)
	return psiPacket(pmtPID, section(0x02, 1, body))
}

// 全サービスを含む TS (PX-Q3U4 など) では、一部の ES の CA 記述子が PID 0x1FFF (ECM なし) になる。これを ECM の PID と
// 見なして答えを待つと、来るはずのない ECM を待ち続けて、溜めた量の上限 (8 MiB、17 Mbps で約 4 秒) まで何も出ない。
func TestStream_CA記述子のPIDが0x1FFFなら_ECMを待たずに出す(t *testing.T) {
	card := &fakeCard{code: 0x0800}
	s := NewStream(card, t.Logf)
	s.SetDecrypt(true)
	plain := pesPayload(1)
	free := tsPacket(0x103, true, 0, pesPayload(3))
	in := concat(
		patPacket(0x100), pmtWithNullCA(0x100, 0x101, 0x102, 0x103),
		ecmPacket(0x102, []byte{1, 2, 3}),
		scrambledES(0x101, oddKey, true, plain),
		free,
	)
	ps := packets(s.Process(in))
	if len(ps) != 5 {
		t.Fatalf("ECM が答えたらすぐに出すはず (0x1FFF の ECM は待たない): %d パケット", len(ps))
	}
	if !bytes.Equal(ps[3][4:], plain) || ps[3][3]>>6 != 0 {
		t.Errorf("復号できない (tsc=%d)", ps[3][3]>>6)
	}
	if !bytes.Equal(ps[4], free) {
		t.Errorf("ECM の無い ES はそのまま")
	}
	if _, u := s.Stats(); u != 0 {
		t.Errorf("復号できなかった数: %d", u)
	}
}

func TestStream_契約が無いと答えたらスクランブルのまま流す(t *testing.T) {
	card := &fakeCard{code: 0xA023}
	s := NewStream(card, t.Logf)
	s.SetDecrypt(true)
	es := scrambledES(0x101, oddKey, true, pesPayload(1))
	out := s.Process(concat(patPacket(0x100), pmtPacket(0x100, 0x101, 0x102, true), ecmPacket(0x102, []byte{1}), es))
	ps := packets(out)
	if len(ps) != 4 || !bytes.Equal(ps[3], es) {
		t.Fatalf("そのまま流すはず")
	}
	if _, u := s.Stats(); u != 0 {
		t.Errorf("契約が無いと答えた ES は「復号できなかった数」に入れない: %d", u)
	}
}

func TestStream_カードが答えなければスクランブルのまま流して止まらない(t *testing.T) {
	card := &fakeCard{err: errors.New("固まった")}
	s := NewStream(card, t.Logf)
	s.SetDecrypt(true)
	es := scrambledES(0x101, oddKey, true, pesPayload(1))
	out := s.Process(concat(patPacket(0x100), pmtPacket(0x100, 0x101, 0x102, true), ecmPacket(0x102, []byte{1}), es))
	if ps := packets(out); len(ps) != 4 || !bytes.Equal(ps[3], es) {
		t.Fatalf("そのまま流すはず: %d", len(ps))
	}
}

func TestStream_鍵が合わなければPESの先頭で見抜いてそのまま流す(t *testing.T) {
	card := &fakeCard{code: 0x0800}
	s := NewStream(card, t.Logf)
	s.SetDecrypt(true)
	wrong := scrambledES(0x101, [8]byte{9, 9, 9, 9, 9, 9, 9, 9}, true, pesPayload(1))
	out := s.Process(concat(patPacket(0x100), pmtPacket(0x100, 0x101, 0x102, true), ecmPacket(0x102, []byte{1}), wrong))
	ps := packets(out)
	if len(ps) != 4 || !bytes.Equal(ps[3], wrong) {
		t.Fatalf("合わない鍵ではスクランブルのまま")
	}
	if _, u := s.Stats(); u != 1 {
		t.Errorf("復号できなかった数: %d", u)
	}
}

func TestStream_CAの無い局は溜めずに通す(t *testing.T) {
	card := &fakeCard{code: 0x0800}
	s := NewStream(card, t.Logf)
	s.SetDecrypt(true)
	free := tsPacket(0x101, true, 0, pesPayload(3))
	out := s.Process(concat(patPacket(0x100), pmtPacket(0x100, 0x101, 0, false), free))
	if ps := packets(out); len(ps) != 3 || !bytes.Equal(ps[2], free) {
		t.Fatalf("PAT と PMT がそろえば流す: %d", len(ps))
	}
	if len(card.calls) != 0 {
		t.Errorf("カードに問い合わせない")
	}
}

func TestStream_鍵の要る利用者がいなければそのまま流す(t *testing.T) {
	card := &fakeCard{code: 0x0800}
	s := NewStream(card, t.Logf)
	s.SetDecrypt(false)
	in := concat(scrambledES(0x101, oddKey, true, pesPayload(1)), []byte{0x47, 2, 3}) // 端数は次の塊へ
	out := s.Process(in)
	if len(out) != 188 || !bytes.Equal(out, in[:188]) || len(card.calls) != 0 {
		t.Errorf("素通しのはず: %d", len(out))
	}
}

func TestStream_パケットの境目が塊とずれても揃える(t *testing.T) {
	card := &fakeCard{code: 0x0800}
	s := NewStream(card, t.Logf)
	in := concat([]byte{0x00, 0x11}, tsPacket(0x200, false, 0, []byte{1}), tsPacket(0x200, false, 0, []byte{2}))
	var out []byte
	for i := 0; i < len(in); i += 100 {
		end := i + 100
		if end > len(in) {
			end = len(in)
		}
		out = append(out, s.Process(in[i:end])...)
	}
	if len(out) != 376 || out[0] != 0x47 || out[188] != 0x47 {
		t.Errorf("先頭のゴミを捨てて 2 パケット: %d", len(out))
	}
}

func TestStream_EMMは自分のカードの番号だけ渡す(t *testing.T) {
	myID := CardID{1, 2, 3, 4, 5, 6}
	card := &fakeCard{code: 0x0800, ids: []CardID{myID}}
	s := NewStream(card, t.Logf)
	s.SetDecrypt(true)
	mine := append([]byte{1, 2, 3, 4, 5, 6, 6}, bytes.Repeat([]byte{0xAA}, 6)...) // 13 バイト
	other := append([]byte{9, 9, 9, 9, 9, 9, 6}, bytes.Repeat([]byte{0xBB}, 6)...)
	emm := append([]byte{0x84, 0xB0, byte(len(mine) + len(other) + 4)}, append(mine, other...)...)
	crc := crc32mpeg(emm)
	emm = append(emm, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
	cat := section(0x01, 0xFFFF, []byte{0x09, 0x04, 0x00, 0x05, 0xE0 | 0x01, 0x00}) // EMM PID 0x100... 0x0100
	s.Process(concat(psiPacket(1, cat), psiPacket(0x100, emm)))
	if len(card.emms) != 1 || !bytes.Equal(card.emms[0], mine) {
		t.Errorf("EMM: %v", card.emms)
	}
}

func TestSecAsm_複数のパケットにまたがるセクション(t *testing.T) {
	big := make([]byte, 400)
	for i := range big {
		big[i] = byte(i)
	}
	sec := section(0x82, 0, big)
	var got [][]byte
	a := &secAsm{}
	p1 := append([]byte{0}, sec[:150]...)
	a.feed(true, p1, func(s []byte) { got = append(got, append([]byte(nil), s...)) })
	a.feed(false, sec[150:330], func(s []byte) { got = append(got, append([]byte(nil), s...)) })
	a.feed(false, append(append([]byte(nil), sec[330:]...), 0xFF, 0xFF), func(s []byte) { got = append(got, append([]byte(nil), s...)) })
	if len(got) != 1 || !bytes.Equal(got[0], sec) || !crcOK(got[0]) {
		t.Fatalf("組み立てられない: %d", len(got))
	}
}

// keyedCard は ECM の中身ごとに違う鍵を返す。
type keyedCard struct {
	byContent map[string]ECMResult
}

func (c *keyedCard) Info() (Info, error) {
	return Info{SystemKey: testSys, CBCInit: testCBC, CASystemID: 5}, nil
}
func (c *keyedCard) ECM(ecm []byte) (ECMResult, error) {
	return c.byContent[string(ecm)], nil
}
func (c *keyedCard) EMM([]byte) {}

// 実機 (PIX-DT295 の NHK) で起きたこと: 選局の直後に溜めている間に、鍵が切り替わった。
// 溜めたぶんを最新の鍵で復号すると、最初の奇数期間が合わなかった。
func TestStream_溜めている間に鍵が切り替わっても到着したときの鍵で復号する(t *testing.T) {
	k1o := [8]byte{1, 1, 1, 1, 1, 1, 1, 1}
	k1e := [8]byte{2, 2, 2, 2, 2, 2, 2, 2}
	k2o := [8]byte{3, 3, 3, 3, 3, 3, 3, 3}
	ecm1, ecm2 := []byte{0xA1}, []byte{0xA2}
	card := &keyedCard{byContent: map[string]ECMResult{
		string(ecm1): {Odd: k1o, Even: k1e, Code: 0x0800},
		string(ecm2): {Odd: k2o, Even: k1e, Code: 0x0800}, // 次の奇数鍵だけ替わる
	}}
	s := NewStream(card, t.Logf)
	s.SetDecrypt(true)

	var plains [][]byte
	es := func(key [8]byte, odd bool, seed byte) []byte {
		p := pesPayload(seed)
		plains = append(plains, p)
		return scrambledES(0x101, key, odd, p)
	}
	// PAT には PMT が 2 つ。2 つ目は来ないので、溜めるのは ready 待ちのまま続く
	pat := psiPacket(0, section(0x00, 1, []byte{0, 1, 0xE1, 0x00, 0, 2, 0xE2, 0x00}))

	var in [][]byte
	in = append(in, es(k1o, true, 1), es(k1o, true, 2), es(k1o, true, 3)) // 最初の ECM より前
	in = append(in, pat, pmtPacket(0x100, 0x101, 0x102, true))
	in = append(in, ecmPacket(0x102, ecm1))
	in = append(in, es(k1o, true, 4), es(k1o, true, 5))
	in = append(in, es(k1e, false, 6), es(k1e, false, 7), es(k1e, false, 8))
	in = append(in, ecmPacket(0x102, ecm2))
	in = append(in, es(k1e, false, 9), es(k1e, false, 10))
	in = append(in, es(k2o, true, 11), es(k2o, true, 12))

	var out []byte
	out = append(out, s.Process(concat(in...))...)
	if len(out) != 0 {
		t.Fatalf("PMT が揃うまで溜める: %d バイト出た", len(out))
	}
	s.patAt = time.Now().Add(-10 * time.Second) // 来ない PMT を待つのをやめさせる
	out = append(out, s.Process(tsPacket(0x300, false, 0, []byte{1}))...)

	ps := packets(out)
	var got [][]byte
	for _, p := range ps {
		if pid := uint16(p[1]&0x1F)<<8 | uint16(p[2]); pid == 0x101 {
			got = append(got, p)
		}
	}
	if len(got) != len(plains) {
		t.Fatalf("ES のパケット: %d (want %d)", len(got), len(plains))
	}
	for i := range got {
		if !bytes.Equal(got[i][4:], plains[i]) || got[i][3]>>6 != 0 {
			t.Errorf("%d 番目のパケットが復号されていない (tsc=%d)", i, got[i][3]>>6)
		}
	}
	if _, u := s.Stats(); u != 0 {
		t.Errorf("復号できなかった数: %d", u)
	}
}

// 偶奇が最初の ECM より前に替わっていたら、替わる前のぶんは鍵が分からないのでスクランブルのまま流す。
func TestStream_最初のECMより前に偶奇が替わっていたら前のぶんは流すだけ(t *testing.T) {
	k1o := [8]byte{1, 1, 1, 1, 1, 1, 1, 1}
	k1e := [8]byte{2, 2, 2, 2, 2, 2, 2, 2}
	ecm1 := []byte{0xA1}
	card := &keyedCard{byContent: map[string]ECMResult{string(ecm1): {Odd: k1o, Even: k1e, Code: 0x0800}}}
	s := NewStream(card, t.Logf)
	s.SetDecrypt(true)

	old := scrambledES(0x101, [8]byte{9, 9, 9, 9, 9, 9, 9, 9}, true, pesPayload(1)) // 前の期間の奇数鍵 (分からない)
	cur := pesPayload(2)
	in := concat(old, scrambledES(0x101, k1e, false, cur), patPacket(0x100), pmtPacket(0x100, 0x101, 0x102, true), ecmPacket(0x102, ecm1))
	ps := packets(s.Process(in))
	var got [][]byte
	for _, p := range ps {
		if pid := uint16(p[1]&0x1F)<<8 | uint16(p[2]); pid == 0x101 {
			got = append(got, p)
		}
	}
	if len(got) != 2 || !bytes.Equal(got[0], old) {
		t.Fatalf("鍵が分からない前の期間は、そのまま流す: %d", len(got))
	}
	if !bytes.Equal(got[1][4:], cur) || got[1][3]>>6 != 0 {
		t.Errorf("ECM をまたいで続く偶数の並びは復号する")
	}
}
