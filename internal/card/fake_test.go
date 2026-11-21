//go:build fakecard

package card

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// TestFakeLink_APDU は、試験カードが APDU に固定の答えを返すことを確かめます。
func TestFakeLink_APDU(t *testing.T) {
	l := &fakeCardLink{}

	// INT (57 バイト。返り値 0x2100、CA 0x0005、システム鍵、初期 CBC)。
	resp, err := l.Transmit(apduFor(false, 0x30, nil))
	if err != nil {
		t.Fatal(err)
	}
	info, err := ParseINT(resp)
	if err != nil {
		t.Fatal(err)
	}
	if info.CASystemID != 0x0005 || info.SystemKey[0] != 0x10 || info.CBCInit[0] != 0xa0 {
		t.Fatalf("INT の中身が違います: %+v", info)
	}

	// IDI (番号は 0000004886718345)。
	resp, err = l.Transmit(apduFor(false, 0x32, nil))
	if err != nil {
		t.Fatal(err)
	}
	_, strs := ParseIDI(resp)
	if len(strs) != 1 || strs[0] != "0000004886718345" {
		t.Fatalf("IDI の番号が違います: %v", strs)
	}

	// ECM (鍵は奇数 0x11、偶数 0x22、返り値 0x0200)。
	resp, err = l.Transmit(apduFor(false, 0x34, []byte{0x01, 0x02, 0x03}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp) < 25 || code(resp) != 0x0200 || resp[6] != 0x11 || resp[14] != 0x22 {
		t.Fatalf("ECM の答えが違います: %x", resp)
	}

	// 契約確認 (0x0800)。
	resp, err = l.Transmit(apduFor(false, 0x3C, []byte{0x12, 0x34, 0xAA}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp) < 6 || code(resp) != 0x0800 {
		t.Fatalf("契約確認の返り値が違います: %x", resp)
	}

	// EMM (0x2100)。
	resp, err = l.Transmit(apduFor(false, 0x36, bytes.Repeat([]byte{0xAA}, 13)))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp) < 6 || code(resp) != 0x2100 {
		t.Fatalf("EMM の返り値が違います: %x", resp)
	}

	// 分からないコマンドは 6A86 で断ります。
	resp, err = l.Transmit(apduFor(false, 0x99, nil))
	if err != nil || !bytes.Equal(resp, []byte{0x6A, 0x86}) {
		t.Fatalf("分からないコマンドの答えが違います: %x, %v", resp, err)
	}

	// 短すぎる APDU と、短すぎる EMM はエラーです。
	if _, err := l.Transmit([]byte{0x90, 0x30}); err == nil {
		t.Error("短い APDU を通しています")
	}
	if _, err := l.Transmit(apduFor(false, 0x36, []byte{0xAA})); err == nil {
		t.Error("短い EMM を通しています")
	}
}

// TestFakeCard_FirstRegardlessOfOrder は、本物の Provider を先に登録しても、
// FAKE_CARD=1 のときは試験カードが先に選ばれ、先に並ぶことを確かめます。
// 登録の順は init の順 (ファイル名の順) に頼っているので、Manager の側の決めで直します。
func TestFakeCard_FirstRegardlessOfOrder(t *testing.T) {
	t.Setenv("FAKE_CARD", "1")
	real := &fakeLink{}
	fp := FakeProvider()
	m := New([]Provider{
		&fakeProvider{readers: []ReaderInfo{{Name: "A", Internal: true}}, links: map[string]*fakeLink{"A": real}},
		fp,
	}, nil, nil, Hooks{})
	info, err := m.Info()
	if err != nil {
		t.Fatal(err)
	}
	if len(info.IDs) != 1 || info.IDs[0] != [6]byte{0x00, 0x01, 0x23, 0x45, 0x67, 0x89} {
		t.Fatalf("試験カードが先に選ばれません: %+v", info)
	}
	if real.count(0x30) != 0 {
		t.Fatal("本物のリーダーに INT を送っています")
	}
	st := m.Status(context.Background())
	readers, _ := st["readers"].([]map[string]any)
	if len(readers) == 0 || readers[0]["name"] != "fake" {
		t.Fatalf("/denpa/card で fake が先頭に並びません: %+v", st)
	}
}

// TestFakeCard_Manager は、FAKE_CARD=1 のときに Manager が試験カードを使うことを確かめます。
func TestFakeCard_Manager(t *testing.T) {
	t.Setenv("FAKE_CARD", "1")
	fp := FakeProvider()
	m := New([]Provider{fp}, nil, nil, Hooks{})

	info, err := m.Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.CASystemID != 0x0005 || len(info.IDs) != 1 || info.IDs[0] != [6]byte{0x00, 0x01, 0x23, 0x45, 0x67, 0x89} {
		t.Fatalf("試験カードの番号が違います: %+v", info)
	}

	res, err := m.ECM([]byte{0x01, 0x02})
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != 0x0200 || res.Odd[0] != 0x11 || res.Even[0] != 0x22 {
		t.Fatalf("試験カードの ECM が違います: %+v", res)
	}

	code, err := m.Confirm(context.Background(), []byte{0xAA}, 0x1234)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0x0800 {
		t.Fatalf("試験カードの契約確認が違います: %#04x", code)
	}

	// EMM は emmLoop が 1 秒に 1 件まで送ります。試験カードは 0x2100 で受け入れます。
	payload := append([]byte{0x00, 0x01, 0x23, 0x45, 0x67, 0x89, 6}, bytes.Repeat([]byte{0xAA}, 6)...)
	m.EMM(payload)
	deadline := time.Now().Add(3 * time.Second)
	for fp.Calls(0x36) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("EMM が試験カードに送られませんでした")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// /denpa/card には、名前が "fake" のリーダーが出ます。
	st := m.Status(context.Background())
	readers, _ := st["readers"].([]map[string]any)
	found := false
	for _, r := range readers {
		if r["name"] == "fake" {
			found = true
			if r["card"] != "B-CAS" || r["active"] != true {
				t.Fatalf("fake リーダーの表示が違います: %+v", r)
			}
			ids, _ := r["ids"].([]string)
			if len(ids) != 1 || ids[0] != "0000004886718345" {
				t.Fatalf("fake リーダーの番号が違います: %+v", r)
			}
		}
	}
	if !found {
		t.Fatalf("fake リーダーが /denpa/card に出ません: %+v", st)
	}
}

// TestFakeCard_Disabled は、FAKE_CARD が無ければ試験カードが出ないことを確かめます
// (タグだけでは有効になりません)。
func TestFakeCard_Disabled(t *testing.T) {
	t.Setenv("FAKE_CARD", "")
	fp := FakeProvider()
	if rs, err := fp.List(); err != nil || len(rs) != 0 {
		t.Fatalf("FAKE_CARD が無いのにリーダーを出しています: %v, %v", rs, err)
	}
	m := New([]Provider{fp}, nil, nil, Hooks{})
	st := m.Status(context.Background())
	if st["ok"] != false {
		t.Fatalf("カードが無いのに ok です: %+v", st)
	}
}
