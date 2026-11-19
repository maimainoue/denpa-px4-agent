package chardev

import (
	"math"
	"testing"
	"unsafe"
)

func TestIoctl番号はptx_ioctl_hのとおり(t *testing.T) {
	cases := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"PTX_SET_SYSTEM_MODE", ptxSetSystemMode, 0x40048d0b},
		{"PTX_SET_CHANNEL", ptxSetChannel, 0x40088d01},
		{"PTX_START_STREAMING", ptxStartStreaming, 0x8d02},
		{"PTX_STOP_STREAMING", ptxStopStreaming, 0x8d03},
		{"PTX_GET_CNR", ptxGetCnr, 0x80088d04},
		{"PTX_ENABLE_LNB_POWER", ptxEnableLnbPower, 0x40048d05},
		{"PTX_DISABLE_LNB_POWER", ptxDisableLnbPower, 0x8d06},
		{"PTXT_SET_PARAMS", ptxtSetParams, 0x4018e702},
		{"PTXT_TUNE", ptxtTune, 0xe704},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: %#x (want %#x)", c.name, c.got, c.want)
		}
	}
}

func TestPtxt構造体の大きさはptx_ioctl_hのとおり(t *testing.T) {
	if n := unsafe.Sizeof(ptxtParams{}); n != 24 {
		t.Errorf("ptxt_params: %d バイト (want 24)", n)
	}
	if n := unsafe.Sizeof(ptxtProp{}); n != 8 {
		t.Errorf("ptxt_additional_param: %d バイト (want 8)", n)
	}
}

func TestTsmfProps(t *testing.T) {
	if p := tsmfProps(0x40, 3); len(p) != 1 || p[0].Code != 2 || p[0].Data != 3 {
		t.Errorf("ISDB-C: %+v", p)
	}
	if p := tsmfProps(0x80, 0); p != nil {
		t.Errorf("J.83B は追加パラメーター無し: %+v", p)
	}
}

func TestCNR(t *testing.T) {
	// P = 0 のとき、多項式の定数項
	db, ok := CNR(5505024, false)
	if !ok || math.Abs(db-3.0965) > 1e-9 {
		t.Errorf("地上波 P=0: %v %v", db, ok)
	}
	db, ok = CNR(3000, true)
	if !ok || math.Abs(db-58.857) > 1e-9 {
		t.Errorf("衛星 P=0: %v %v", db, ok)
	}
	if _, ok := CNR(0, false); ok {
		t.Error("値が 0 なら null")
	}
	if _, ok := CNR(2999, true); ok {
		t.Error("3000 未満なら null")
	}
	// 値が小さい (雑音が小さい) ほど C/N が大きい
	lo, _ := CNR(200000, false)
	hi, _ := CNR(20000, false)
	if hi <= lo {
		t.Errorf("単調ではない: %v %v", lo, hi)
	}
	// 実際の受信で出る範囲 (20〜35 dB 程度) に入る値がある
	found := false
	for raw := uint32(1000); raw < 400000; raw += 1000 {
		if db, ok := CNR(raw, false); ok && db > 24 && db < 30 {
			found = true
		}
	}
	if !found {
		t.Error("実用の範囲の C/N が出ない")
	}
}
