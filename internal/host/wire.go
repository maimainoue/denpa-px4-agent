// Package host は Windows と macOS の DriverHost_PX4 のクライアント (仕様書 5 章)。
// コマンドの構造体は px4_drv の host/common/command.hpp・card_command.hpp の定義
// (#pragma pack(push, 8)、リトルエンディアン) をそのまま送る。通信路 (名前付きパイプ・UNIX ドメインソケット) だけ OS ごと。
package host

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
)

// 制御のコマンド番号 (5.2)。
const (
	cmdGetVersion   = 1
	cmdOpen         = 8
	cmdClose        = 9
	cmdGetInfo      = 10
	cmdSetCapture   = 11
	cmdSetParams    = 17
	cmdClearParams  = 18
	cmdTune         = 19
	cmdCheckLock    = 20
	cmdSetLnbVolt   = 24
	cmdReadStats    = 32
	cmdVersionValue = 0x00040004

	statusOK   = 1
	statusFail = 2

	paramBandwidth  = 1
	paramTSMFStream = 2
	paramStreamID   = 16

	statSignalStrength = 1
	statCNR            = 2
)

// TS のパイプの DataCmd。
const (
	dataSetDataID = 1
	dataPurge     = 8
)

// カードのコマンド (5.2)。
const (
	cardGetVersion   = 1
	cardListReaders  = 2
	cardConnect      = 3
	cardDisconnect   = 5
	cardStatus       = 6
	cardTransmit     = 8
	cardVersionValue = 0x00020001

	cardShared    = 1
	cardExclusive = 2

	cardErrNone = 0
)

// cardErrNames は card_command.hpp の error。
var cardErrNames = map[uint32]string{
	1: "INVALID_PARAMETER", 2: "NO_READER", 3: "NO_CARD", 4: "BUSY", 5: "REMOVED",
	6: "TIMEOUT", 7: "PROTOCOL", 8: "INSUFFICIENT_BUFFER", 9: "INTERNAL",
}

// 方式のビット。
const (
	SystemT   = 0x10
	SystemS   = 0x20
	SystemC   = 0x40
	SystemJ83 = 0x80
)

// Layout は wchar_t の大きさ (Windows 2、macOS 4) で決まる構造体の大きさ。
type Layout struct{ W int }

var (
	// Windows は UTF-16LE の 2 バイト、macOS は UTF-32LE の 4 バイト。
	LayoutWindows = Layout{W: 2}
	LayoutMac     = Layout{W: 4}
)

// ReceiverInfoSize は ReceiverInfo (428 / 812)。
func (l Layout) ReceiverInfoSize() int { return 96*l.W + 16 + 96*l.W + 16 + 12 }

// OpenSize は CtrlOpenCmd と CtrlReceiverInfoCmd (436 / 820)。
func (l Layout) OpenSize() int { return 8 + l.ReceiverInfoSize() }

// CardSize はカードの Command (4,952 / 9,304)。
func (l Layout) CardSize() int { return l.cardOffsets().total }

type cardOff struct {
	readers, readerName, shareMode, disposition, present, initialized, atr, atrLen, data, dataLen, dataCap, total int
}

func (l Layout) cardOffsets() cardOff {
	var o cardOff
	o.readers = 32 // code, status, error, version, reader_count (5×4)、詰め物 4、reader_generation u64
	o.readerName = o.readers + 16*128*l.W
	o.shareMode = o.readerName + 128*l.W
	o.disposition = o.shareMode + 4
	o.present = o.disposition + 4
	o.initialized = o.present + 1
	o.atr = o.initialized + 1
	o.atrLen = (o.atr + 33 + 3) &^ 3
	o.data = o.atrLen + 4
	o.dataLen = o.data + 512
	o.dataCap = o.dataLen + 4
	o.total = o.dataCap + 4
	return o
}

// GUID は {aa8de3fc-004d-49a4-b976-4250f15a976d} を 16 バイト (Data1 u32・Data2 u16・Data3 u16・Data4 [8]u8) にする。
func GUID(s string) ([16]byte, error) {
	var g [16]byte
	h := strings.NewReplacer("{", "", "}", "", "-", "").Replace(strings.TrimSpace(s))
	if len(h) != 32 {
		return g, fmt.Errorf("GUID を読めません (%s)", s)
	}
	var b [16]byte
	for i := 0; i < 16; i++ {
		var v byte
		if _, err := fmt.Sscanf(h[2*i:2*i+2], "%02x", &v); err != nil {
			return g, fmt.Errorf("GUID を読めません (%s)", s)
		}
		b[i] = v
	}
	binary.LittleEndian.PutUint32(g[0:], binary.BigEndian.Uint32(b[0:]))
	binary.LittleEndian.PutUint16(g[4:], binary.BigEndian.Uint16(b[4:]))
	binary.LittleEndian.PutUint16(g[6:], binary.BigEndian.Uint16(b[6:]))
	copy(g[8:], b[8:])
	return g, nil
}

func putWChars(dst []byte, s string, w int) {
	if w == 2 {
		for i, u := range utf16.Encode([]rune(s)) {
			if 2*i+2 > len(dst) {
				return
			}
			binary.LittleEndian.PutUint16(dst[2*i:], u)
		}
		return
	}
	for i, r := range []rune(s) {
		if 4*i+4 > len(dst) {
			return
		}
		binary.LittleEndian.PutUint32(dst[4*i:], uint32(r))
	}
}

func getWChars(src []byte, w int) string {
	if w == 2 {
		var us []uint16
		for i := 0; i+2 <= len(src); i += 2 {
			u := binary.LittleEndian.Uint16(src[i:])
			if u == 0 {
				break
			}
			us = append(us, u)
		}
		return string(utf16.Decode(us))
	}
	var rs []rune
	for i := 0; i+4 <= len(src); i += 4 {
		r := binary.LittleEndian.Uint32(src[i:])
		if r == 0 {
			break
		}
		rs = append(rs, rune(r))
	}
	return string(rs)
}

func header(n int, cmd uint32) []byte {
	b := make([]byte, n)
	binary.LittleEndian.PutUint32(b[0:], cmd)
	return b
}

func respStatus(b []byte) uint32 { return binary.LittleEndian.Uint32(b[4:]) }

// ---- 制御のコマンドを作る ----

func versionReq() []byte { return header(16, cmdGetVersion) }

func openReq(l Layout, guid [16]byte, index int32, systems uint32) []byte {
	b := header(l.OpenSize(), cmdOpen)
	p := 8 + 96*l.W // device_name の後ろが device_guid
	copy(b[p:], guid[:])
	p += 16 + 96*l.W + 16 // receiver_name、receiver_guid を飛ばす
	binary.LittleEndian.PutUint32(b[p:], systems)
	binary.LittleEndian.PutUint32(b[p+4:], uint32(index))
	return b
}

// openDataID は OPEN の応答の data_id。
func openDataID(l Layout, resp []byte) uint32 {
	p := 8 + 96*l.W + 16 + 96*l.W + 16 + 8
	return binary.LittleEndian.Uint32(resp[p:])
}

func closeReq() []byte { return header(8, cmdClose) }

func captureReq(on bool) []byte {
	b := header(12, cmdSetCapture)
	if on {
		b[8] = 1
	}
	return b
}

// Param は SET_PARAMS の追加パラメータ。
type Param struct{ Type, Value uint32 }

func paramsReq(system, freqKHz uint32, params []Param) []byte {
	n := 28
	if len(params) > 1 {
		n += 8 * (len(params) - 1)
	}
	b := header(n, cmdSetParams)
	binary.LittleEndian.PutUint32(b[8:], system)
	binary.LittleEndian.PutUint32(b[12:], freqKHz)
	binary.LittleEndian.PutUint32(b[16:], uint32(len(params)))
	for i, p := range params {
		binary.LittleEndian.PutUint32(b[20+8*i:], p.Type)
		binary.LittleEndian.PutUint32(b[24+8*i:], p.Value)
	}
	return b
}

func tuneReq(timeoutMs uint32) []byte {
	b := header(12, cmdTune)
	binary.LittleEndian.PutUint32(b[8:], timeoutMs)
	return b
}

func checkLockReq() []byte { return header(12, cmdCheckLock) }

func lnbReq(v int32) []byte {
	b := header(12, cmdSetLnbVolt)
	binary.LittleEndian.PutUint32(b[8:], uint32(v))
	return b
}

func statsReq(typ uint32) []byte {
	b := header(20, cmdReadStats)
	binary.LittleEndian.PutUint32(b[8:], 1)
	binary.LittleEndian.PutUint32(b[12:], typ)
	return b
}

func dataCmd(cmd, id uint32) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint32(b[0:], cmd)
	binary.LittleEndian.PutUint32(b[4:], id)
	return b
}

// ---- カードのコマンド ----

func (l Layout) cardReq(code uint32) []byte { return header(l.CardSize(), code) }

func (l Layout) cardError(b []byte) uint32 { return binary.LittleEndian.Uint32(b[8:]) }

func (l Layout) cardVersion(b []byte) uint32 { return binary.LittleEndian.Uint32(b[12:]) }

func (l Layout) cardReaders(b []byte) []string {
	o := l.cardOffsets()
	n := int(binary.LittleEndian.Uint32(b[16:]))
	if n > 16 {
		n = 16
	}
	var out []string
	for i := 0; i < n; i++ {
		out = append(out, getWChars(b[o.readers+i*128*l.W:o.readers+(i+1)*128*l.W], l.W))
	}
	return out
}

func (l Layout) setReaderName(b []byte, name string) {
	o := l.cardOffsets()
	putWChars(b[o.readerName:o.readerName+128*l.W], name, l.W)
}

func (l Layout) setShareMode(b []byte, mode uint32) {
	binary.LittleEndian.PutUint32(b[l.cardOffsets().shareMode:], mode)
}

func (l Layout) setAPDU(b []byte, apdu []byte) error {
	o := l.cardOffsets()
	if len(apdu) > 512 {
		return errors.New("APDU が長すぎます")
	}
	copy(b[o.data:], apdu)
	binary.LittleEndian.PutUint32(b[o.dataLen:], uint32(len(apdu)))
	binary.LittleEndian.PutUint32(b[o.dataCap:], 512)
	return nil
}

func (l Layout) cardData(b []byte) ([]byte, error) {
	o := l.cardOffsets()
	n := int(binary.LittleEndian.Uint32(b[o.dataLen:]))
	if n > 512 {
		return nil, errors.New("応答の長さが不正です")
	}
	return append([]byte(nil), b[o.data:o.data+n]...), nil
}

func (l Layout) cardPresent(b []byte) bool { return b[l.cardOffsets().present] != 0 }
