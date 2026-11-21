package cardshare

import (
	"bytes"
	"slices"
	"testing"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/b25"
)

func TestWireInit(t *testing.T) {
	info := b25.Info{
		CASystemID: 0x0005,
		IDs:        []b25.CardID{{0x00, 0x01, 0x23, 0x45, 0x67, 0x89}, {0x00, 0x01, 0x23, 0x45, 0x67, 0x8a}},
	}
	for i := range info.SystemKey {
		info.SystemKey[i] = byte(0x10 + i)
	}
	for i := range info.CBCInit {
		info.CBCInit[i] = byte(0xa0 + i)
	}
	body := PackInit(info)
	if len(body) != 44+16 {
		t.Fatalf("init の長さが違います: %d バイト", len(body))
	}
	back, err := ReadInit(body)
	if err != nil {
		t.Fatal(err)
	}
	if back.SystemKey != info.SystemKey || back.CBCInit != info.CBCInit ||
		back.CASystemID != info.CASystemID || !slices.Equal(back.IDs, info.IDs) {
		t.Fatalf("init の往復が違います: %+v", back)
	}
	// カード番号が無くても 44 バイトです。
	empty := b25.Info{CASystemID: 0x0005}
	back, err = ReadInit(PackInit(empty))
	if err != nil || len(back.IDs) != 0 {
		t.Fatalf("空の init が読めません: %+v, %v", back, err)
	}
	if _, err := ReadInit(body[:43]); err == nil {
		t.Fatal("短い init なのに通りました")
	}
}

func TestWireECM(t *testing.T) {
	answer := b25.ECMResult{Code: 0x0200}
	for i := range answer.Odd {
		answer.Odd[i] = 0x11
		answer.Even[i] = 0x22
	}
	body := PackECM(answer)
	if len(body) != 18 {
		t.Fatalf("ECM の応答の長さが違います: %d バイト", len(body))
	}
	back, err := ReadECM(body)
	if err != nil {
		t.Fatal(err)
	}
	if back != answer {
		t.Fatalf("ECM の応答の往復が違います: %+v", back)
	}
	if _, err := ReadECM(body[:17]); err == nil {
		t.Fatal("短い ECM の応答なのに通りました")
	}
}

func TestWireCode(t *testing.T) {
	for _, code := range []uint16{0x2100, 0x0800, 0x0000} {
		back, err := ReadCode(PackCode(code), "契約確認")
		if err != nil || back != code {
			t.Fatalf("code 0x%04x の往復が違います: 0x%04x, %v", code, back, err)
		}
	}
	if _, err := ReadCode([]byte{0x08}, "契約確認"); err == nil {
		t.Fatal("短い code なのに通りました")
	}
}

func TestCheckConfirm(t *testing.T) {
	if err := CheckConfirm(nil); err == nil {
		t.Fatal("空の verification なのに通りました")
	}
	if err := CheckConfirm(make([]byte, 253)); err != nil {
		t.Fatal(err)
	}
	if err := CheckConfirm(make([]byte, 254)); err == nil {
		t.Fatal("254 バイトの verification なのに通りました")
	}
}

func TestReadUpTo(t *testing.T) {
	body, err := ReadUpTo(bytes.NewReader([]byte{1, 2, 3}), 255)
	if err != nil || !bytes.Equal(body, []byte{1, 2, 3}) {
		t.Fatalf("短い本文が読めません: %v, %v", body, err)
	}
	// 上限ちょうどは読めます。
	body, err = ReadUpTo(bytes.NewReader(make([]byte, 255)), 255)
	if err != nil || len(body) != 255 {
		t.Fatalf("上限ちょうどの本文が読めません: %d バイト, %v", len(body), err)
	}
	// 上限を超えたら nil です。
	body, err = ReadUpTo(bytes.NewReader(make([]byte, 256)), 255)
	if err != nil || body != nil {
		t.Fatalf("上限を超えたのに読めました: %v, %v", len(body), err)
	}
}
