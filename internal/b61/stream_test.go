package b61

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// ---- 試験用の入力を作る道具 ----

func u16(v int) []byte { return []byte{byte(v >> 8), byte(v)} }

// mmtpPacket は MMTP パケット。ctrl が 2 か 3 なら、スクランブル制御の拡張ヘッダーを付ける。
func mmtpPacket(pid uint16, ptype byte, seq uint32, ctrl byte, payload []byte) []byte {
	flags := byte(0)
	if ctrl != 0 {
		flags |= 2
	}
	b := []byte{flags, ptype}
	b = append(b, u16(int(pid))...)
	b = append(b, 0, 0, 0, 0) // タイムスタンプ
	b = append(b, byte(seq>>24), byte(seq>>16), byte(seq>>8), byte(seq))
	if ctrl != 0 {
		b = append(b, 0, 0)          // 拡張の種別 0
		b = append(b, u16(4+1)...)   // 拡張の長さ
		b = append(b, 0x80, 0x01)    // 終端・種別 1 (スクランブル制御)
		b = append(b, u16(1)...)     // 値の長さ
		b = append(b, byte(ctrl)<<3) // 制御
	}
	return append(b, payload...)
}

// tlvCompressed は圧縮 IP (ヘッダー型 0x61) の TLV パケット。
func wrapShort(mmtp []byte) []byte {
	body := append([]byte{0x00, 0x00, hdrPayload}, mmtp...)
	b := []byte{tlvSync, tlvCompressed}
	b = append(b, u16(len(body))...)
	return append(b, body...)
}

// tlvFull は圧縮 IP (ヘッダー型 0x60。IPv6・UDP のヘッダーつき) の TLV パケット。
func tlvFull(mmtp []byte) []byte {
	body := append([]byte{0x00, 0x00, hdrFull}, make([]byte, 38+4)...)
	body = append(body, mmtp...)
	b := []byte{tlvSync, tlvCompressed}
	b = append(b, u16(len(body))...)
	return append(b, body...)
}

// signal は信号のパケット (断片なし)。
func signal(pid uint16, seq uint32, msg []byte) []byte {
	return mmtpPacket(pid, 2, seq, 0, append([]byte{0x00, 0x00}, msg...))
}

// paMessage は PA メッセージ (長さは 32 ビット)。
func paMessage(tableID byte, version byte, table []byte) []byte {
	inner := []byte{0} // 表の数
	inner = append(inner, tableID, version)
	inner = append(inner, u16(len(table))...)
	inner = append(inner, table...)
	m := []byte{0x00, 0x00, 0x00}
	m = append(m, u16(0)...) // 長さの上位 16 ビット
	m = append(m, u16(len(inner))...)
	return append(m, inner...)
}

func plt(serviceID int, mptPID uint16) []byte {
	t := []byte{1, 2}
	t = append(t, u16(serviceID)...)
	t = append(t, 0x00)
	t = append(t, u16(int(mptPID))...)
	return paMessage(0x80, 0, t)
}

func caDesc(ecmPID int) []byte {
	d := []byte{0x80, 0x04, 5}
	d = append(d, u16(caSystemACAS)...)
	d = append(d, 0x00)
	return append(d, u16(ecmPID)...)
}

// mpt は番組 1 つ。ecmPID が 0xFFFF 以外なら、番組に CA 記述子を付ける。assets は (packet id, スクランブルか)。
func mpt(ecmPID int, version byte, assets map[uint16]bool) []byte {
	t := []byte{0x00, 2, 0x00, 0x01} // モード・ID の長さ・ID
	var desc []byte
	if ecmPID != 0xFFFF {
		desc = caDesc(ecmPID)
	}
	t = append(t, u16(len(desc))...)
	t = append(t, desc...)
	t = append(t, byte(len(assets)))
	for pid, scrambled := range assets {
		t = append(t, 0x00)               // 識別子の型
		t = append(t, 0, 0, 0, 0)         // ID の体系
		t = append(t, 2, 0x00, 0x01)      // ID の長さ・ID
		t = append(t, 'h', 'e', 'v', '1') // asset の型
		t = append(t, 0x00)               // 予約
		t = append(t, 1, 0x00)            // 場所の数・型
		t = append(t, u16(int(pid))...)
		var ad []byte
		if !scrambled {
			ad = caDesc(0xFFFF)
		}
		t = append(t, u16(len(ad))...)
		t = append(t, ad...)
	}
	return paMessage(0x20, version, t)
}

// ecmMessage は M2 メッセージの ECM (表 0x82)。data はカードに渡る中身。
func ecmMessage(version byte, data []byte) []byte {
	body := []byte{0x00, 0x00, version<<1 | 1, 0, 0}
	body = append(body, data...)
	body = append(body, 0, 0, 0, 0) // CRC32
	sec := []byte{0x82}
	sec = append(sec, u16(0xB000|len(body))...)
	sec = append(sec, body...)
	m := []byte{0x80, 0x02, 0x00}
	m = append(m, u16(len(sec))...)
	return append(m, sec...)
}

func catMessage(emmPID int) []byte {
	desc := caDesc(emmPID)
	sec := []byte{0x86, 0x00}
	sec = append(sec, u16(0xB000|len(desc))...)
	sec = append(sec, desc...)
	m := []byte{0x80, 0x01, 0x00}
	m = append(m, u16(len(sec))...)
	return append(m, sec...)
}

func emmMessage(entries ...[]byte) []byte {
	body := []byte{0x00, 0x00, 1, 0, 0}
	for _, e := range entries {
		body = append(body, e...)
	}
	body = append(body, 0, 0, 0, 0)
	sec := []byte{0x84}
	sec = append(sec, u16(0xB000|len(body))...)
	sec = append(sec, body...)
	m := []byte{0x80, 0x02, 0x00} // M2 セクションのメッセージ (ECM と同じ。0x8003 は長さが 32 ビット)
	m = append(m, u16(len(sec))...)
	return append(m, sec...)
}

func emmEntry(module uint64, body []byte) []byte {
	e := []byte{byte(module >> 40), byte(module >> 32), byte(module >> 24), byte(module >> 16), byte(module >> 8), byte(module)}
	e = append(e, byte(len(body)))
	return append(e, body...)
}

// scrambledMFU はデータのパケット。MPU ペイロードのヘッダー (8 バイト) のあとを、鍵で暗号化する。
func scrambledMFU(pid uint16, seq uint32, ctrl byte, key [16]byte, plain []byte) []byte {
	var iv [16]byte
	iv[0], iv[1] = byte(pid>>8), byte(pid)
	iv[2], iv[3], iv[4], iv[5] = byte(seq>>24), byte(seq>>16), byte(seq>>8), byte(seq)
	enc := append([]byte(nil), plain...)
	blk, _ := aes.NewCipher(key[:])
	cipher.NewCTR(blk, iv[:]).XORKeyStream(enc, enc)
	return mmtpPacket(pid, 0, seq, ctrl, append(make([]byte, 8), enc...))
}

// ---- 偽のカード ----

type fakeCard struct {
	mu       sync.Mutex
	module   uint64
	keys     [][32]byte // ECM の呼び出しごとに順に返す (足りなければ最後)
	err      error
	delay    time.Duration
	ecms     [][]byte
	emms     [][]byte
	modErr   error
	modCalls int
}

func (c *fakeCard) B61Module() (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.modCalls++
	return c.module, c.modErr
}

func (c *fakeCard) B61ECM(ecm []byte) ([32]byte, error) {
	c.mu.Lock()
	n := len(c.ecms)
	c.ecms = append(c.ecms, append([]byte(nil), ecm...))
	err, d := c.err, c.delay
	k := c.keys[min(n, len(c.keys)-1)]
	c.mu.Unlock()
	time.Sleep(d)
	return k, err
}

func (c *fakeCard) EMM(p []byte) {
	c.mu.Lock()
	c.emms = append(c.emms, append([]byte(nil), p...))
	c.mu.Unlock()
}

func keys32(odd, even byte) [32]byte {
	var k [32]byte
	for i := 0; i < 16; i++ {
		k[i], k[16+i] = odd+byte(i), even+byte(i)
	}
	return k
}

func oddOf(k [32]byte) (o [16]byte)  { copy(o[:], k[:16]); return }
func evenOf(k [32]byte) (e [16]byte) { copy(e[:], k[16:]); return }

var sprintf = fmt.Sprintf

func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func newTestStream(c CardService) (*Stream, *[]string) {
	var logs []string
	var mu sync.Mutex
	s := NewStream(c, func(f string, a ...any) {
		mu.Lock()
		logs = append(logs, sprintf(f, a...))
		mu.Unlock()
	})
	s.SetDecrypt(true)
	return s, &logs
}

var ecmData = bytes.Repeat([]byte{0xA5}, 30)

// ---- 試験 ----

func TestTLV_圧縮IPの2つのヘッダー型を読んで復号する(t *testing.T) {
	k := keys32(0x10, 0x40)
	c := &fakeCard{module: 0x09219EA46327, keys: [][32]byte{k}}
	s, _ := newTestStream(c)
	plain := bytes.Repeat([]byte("MFU-DATA"), 20)
	in := cat(
		wrapShort(signal(0x0000, 1, plt(0x0400, 0x8001))),
		tlvFull(signal(0x8001, 1, mpt(0x9001, 0, map[uint16]bool{0x0100: true}))),
		// 鍵が届く前のデータ (溜まる)
		wrapShort(scrambledMFU(0x0100, 1, 2, evenOf(k), plain)),
		wrapShort(signal(0x9001, 1, ecmMessage(3, ecmData))),
		// 鍵が届いたあとのデータ (奇数の鍵)
		wrapShort(scrambledMFU(0x0100, 2, 3, oddOf(k), plain)),
	)
	out := s.Process(in)
	if len(out) == 0 {
		t.Fatalf("出力が無い: %v", s)
	}
	// 出力の中の MFU のパケット 2 つを探して、中身が元に戻っているか
	n := 0
	for i := 0; i+4 < len(out); {
		l := int(out[i+2])<<8 | int(out[i+3])
		pkt := out[i : i+4+l]
		if pkt[1] == tlvCompressed {
			mm := pkt[4+hdrPayloadOffset:]
			if len(mm) > 12 && int(mm[2])<<8|int(mm[3]) == 0x0100 {
				n++
				if mm[0]&2 == 0 {
					t.Error("拡張ヘッダーが消えた")
				}
				if c := (mm[12+4+4] >> 3) & 3; c != 0 {
					t.Errorf("スクランブルの印が消えていない: %d", c)
				}
				data := mm[len(mm)-len(plain):]
				if !bytes.Equal(data, plain) {
					t.Errorf("復号できていない: %q", data[:16])
				}
			}
		}
		i += 4 + l
	}
	if n != 2 {
		t.Errorf("MFU のパケットは 2 つのはず: %d", n)
	}
	if len(c.ecms) != 1 || !bytes.Equal(c.ecms[0], ecmData) {
		t.Errorf("カードに渡った ECM: %x", c.ecms)
	}
	if s.Stats.Decrypted.Load() != 2 {
		t.Errorf("復号した数: %d", s.Stats.Decrypted.Load())
	}
}

func TestTLV_壊れた入力と欠けた塊を扱う(t *testing.T) {
	k := keys32(0x10, 0x40)
	c := &fakeCard{module: 1, keys: [][32]byte{k}}
	s, _ := newTestStream(c)
	good := wrapShort(signal(0x0000, 1, plt(0x0400, 0x8001)))
	// 同期バイトの前のごみ・型が不正な TLV・扱えないヘッダー型
	badHdr := []byte{tlvSync, tlvCompressed, 0, 5, 0, 0, 0x99, 1, 2}
	in := cat([]byte{1, 2, 3}, []byte{tlvSync, 0x77, 0, 0}, badHdr, good)
	var out []byte
	// 1 バイトずつ入れても同じ
	for i := range in {
		out = append(out, s.Process(in[i:i+1])...)
	}
	// 初期バッファの途中なので、まだ出ない。Flush で出る
	out = append(out, s.Flush()...)
	if !bytes.Equal(out, good) {
		t.Errorf("good だけが出るはず:\n got %x\nwant %x", out, good)
	}
}

func TestTLV_スクランブルされていない番組は溜めずに通す(t *testing.T) {
	c := &fakeCard{module: 1, keys: [][32]byte{keys32(1, 2)}}
	s, _ := newTestStream(c)
	in := cat(
		wrapShort(signal(0x0000, 1, plt(0x0400, 0x8001))),
		wrapShort(signal(0x8001, 1, mpt(0xFFFF, 0, map[uint16]bool{0x0100: false}))),
		wrapShort(mmtpPacket(0x0100, 0, 1, 0, make([]byte, 40))),
	)
	out := s.Process(in)
	if !bytes.Equal(out, in) {
		t.Errorf("そのまま出るはず (%d / %d バイト)", len(out), len(in))
	}
	if len(c.ecms) != 0 {
		t.Error("スクランブルが無いのに、カードに問い合わせた")
	}
}

func TestTLV_カードが断れば鍵なしで流し続ける(t *testing.T) {
	k := keys32(0x10, 0x40)
	c := &fakeCard{module: 1, keys: [][32]byte{k}, err: errors.New("契約がありません")}
	s, logs := newTestStream(c)
	plain := bytes.Repeat([]byte("X"), 64)
	pkt := wrapShort(scrambledMFU(0x0100, 1, 2, evenOf(k), plain))
	in := cat(
		wrapShort(signal(0x0000, 1, plt(0x0400, 0x8001))),
		wrapShort(signal(0x8001, 1, mpt(0x9001, 0, map[uint16]bool{0x0100: true}))),
		wrapShort(signal(0x9001, 1, ecmMessage(1, ecmData))),
		pkt,
	)
	out := s.Process(in)
	out = append(out, s.Process(pkt)...)
	if !bytes.HasSuffix(out, pkt) {
		t.Errorf("スクランブルされたまま流れるはず")
	}
	if s.Stats.Decrypted.Load() != 0 || s.Stats.Undecrypted.Load() == 0 {
		t.Errorf("数: %d / %d", s.Stats.Decrypted.Load(), s.Stats.Undecrypted.Load())
	}
	found := false
	for _, l := range *logs {
		if bytes.Contains([]byte(l), []byte("契約がありません")) {
			found = true
		}
	}
	if !found {
		t.Errorf("理由がログに無い: %v", *logs)
	}
}

func TestTLV_鍵が変わる瞬間は新しい鍵を待つ(t *testing.T) {
	// ECM 1: 偶数が使用中、奇数が次の鍵。ECM 2 (奇数の期間の始まり): 奇数は同じで、偶数が新しい
	k1 := keys32(0x10, 0x40)
	k2 := keys32(0x10, 0x90)
	c := &fakeCard{module: 1, keys: [][32]byte{k1, k2}}
	s, _ := newTestStream(c)
	plain := bytes.Repeat([]byte("Y"), 48)
	head := cat(
		wrapShort(signal(0x0000, 1, plt(0x0400, 0x8001))),
		wrapShort(signal(0x8001, 1, mpt(0x9001, 0, map[uint16]bool{0x0100: true}))),
		wrapShort(signal(0x9001, 1, ecmMessage(1, ecmData))),
		wrapShort(scrambledMFU(0x0100, 1, 2, evenOf(k1), plain)),
	)
	if out := s.Process(head); !bytes.HasSuffix(out, plain) {
		t.Fatalf("最初の偶数のパケットを復号できていない")
	}
	// 次の ECM。応答は遅い
	c.mu.Lock()
	c.delay = 400 * time.Millisecond
	c.mu.Unlock()
	s.Process(wrapShort(signal(0x9001, 2, ecmMessage(2, ecmData))))
	// 使用中の奇数のパケットは、応答を待たずに、控えた鍵で復号する
	t0 := time.Now()
	out := s.Process(wrapShort(scrambledMFU(0x0100, 2, 3, oddOf(k1), plain)))
	if d := time.Since(t0); d > 200*time.Millisecond {
		t.Errorf("使用中の鍵のパケットが待たされた (%v)", d)
	}
	if !bytes.HasSuffix(out, plain) {
		t.Errorf("奇数のパケットを復号できていない")
	}
	// 新しい偶数のパケットは、ECM 2 の応答を待って、新しい鍵で復号する
	t0 = time.Now()
	out = s.Process(wrapShort(scrambledMFU(0x0100, 3, 2, evenOf(k2), plain)))
	if d := time.Since(t0); d < 200*time.Millisecond {
		t.Errorf("新しい鍵を待たなかった (%v)", d)
	}
	if !bytes.HasSuffix(out, plain) {
		t.Errorf("新しい偶数鍵で復号できていない")
	}
	if s.Stats.Undecrypted.Load() != 0 {
		t.Errorf("復号できなかったパケットがある: %d", s.Stats.Undecrypted.Load())
	}
}

func TestTLV_復号しない間は素通し(t *testing.T) {
	c := &fakeCard{module: 1, keys: [][32]byte{keys32(1, 2)}}
	s, _ := newTestStream(c)
	s.SetDecrypt(false)
	in := cat(wrapShort(signal(0x0000, 1, plt(0x0400, 0x8001))), []byte{9, 9, 9})
	if out := s.Process(in); !bytes.Equal(out, in) {
		t.Error("素通しのはず")
	}
	if c.modCalls != 0 {
		t.Error("復号しないのに、カードを使った")
	}
}

func TestTLV_A_CASのカードでなければ素通し(t *testing.T) {
	c := &fakeCard{modErr: errors.New("A-CAS のカードが要ります")}
	s, logs := newTestStream(c)
	in := wrapShort(signal(0x0000, 1, plt(0x0400, 0x8001)))
	if out := s.Process(in); !bytes.Equal(out, in) {
		t.Error("素通しのはず")
	}
	s.Process(in)
	if c.modCalls != 1 {
		t.Errorf("10 秒は探し直さない: %d 回", c.modCalls)
	}
	if len(*logs) != 1 {
		t.Errorf("理由は 1 回だけ: %v", *logs)
	}
}

func TestEMM_自分のカードの分だけ渡す(t *testing.T) {
	c := &fakeCard{module: 0x112233445566, keys: [][32]byte{keys32(1, 2)}}
	s, _ := newTestStream(c)
	mine := emmEntry(0x112233445566, []byte{1, 2, 3})
	other := emmEntry(0x998877665544, []byte{4, 5, 6})
	in := cat(
		wrapShort(signal(0x0000, 1, plt(0x0400, 0x8001))),
		wrapShort(signal(0x0001, 1, catMessage(0x9100))),
		wrapShort(signal(0x9100, 1, emmMessage(other, mine))),
	)
	s.Process(in)
	if len(c.emms) != 1 || !bytes.Equal(c.emms[0], mine) {
		t.Errorf("渡った EMM: %x", c.emms)
	}
}

func TestSI_断片を組み立てる(t *testing.T) {
	c := &fakeCard{module: 1, keys: [][32]byte{keys32(1, 2)}}
	s, _ := newTestStream(c)
	msg := plt(0x0400, 0x8001)
	half := len(msg) / 2
	frag := func(ind byte, seq uint32, data []byte) []byte {
		return wrapShort(mmtpPacket(0x0000, 2, seq, 0, append([]byte{ind << 6, 0}, data...)))
	}
	s.Process(cat(frag(1, 1, msg[:half]), frag(3, 2, msg[half:])))
	if len(s.programs) != 1 || s.programs[0x8001] == nil || s.programs[0x8001].serviceID != 0x0400 {
		t.Errorf("番組を読めていない: %v", s)
	}
}

func TestMPT_スクランブルされたassetだけ覚える(t *testing.T) {
	c := &fakeCard{module: 1, keys: [][32]byte{keys32(1, 2)}}
	s, _ := newTestStream(c)
	s.Process(cat(
		wrapShort(signal(0x0000, 1, plt(0x0400, 0x8001))),
		wrapShort(signal(0x8001, 1, mpt(0x9001, 0, map[uint16]bool{0x0100: true, 0x0101: true, 0x0102: false}))),
	))
	if len(s.assets) != 2 || s.assets[0x0100] == nil || s.assets[0x0101] == nil || s.assets[0x0102] != nil {
		t.Errorf("asset: %v", s.assets)
	}
	if s.programs[0x8001].ecmPID != 0x9001 || s.ecms[0x9001] == nil {
		t.Errorf("ECM の packet id を読めていない")
	}
}

func TestDescLenBytes(t *testing.T) {
	for tag, want := range map[uint16]int{0x8004: 1, 0x4000: 2, 0x6FFF: 2, 0x7000: 4, 0x7FFF: 4, 0xF000: 2, 0x0001: 1} {
		if got := descLenBytes(tag); got != want {
			t.Errorf("%#x: %d (want %d)", tag, got, want)
		}
	}
}
