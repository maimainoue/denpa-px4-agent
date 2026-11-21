// Package cardshare は鍵の配り・貰いです。拠点のあいだで鍵をやり取りします。
// 形式は denpa の付属のエージェントと同じ wire で、仕様書 9.5 の表のとおりです。
package cardshare

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/b25"
)

// ErrTooLarge は本文が上限を超えたときのエラーです。空の本文 (長さ 0) と区別します。
var ErrTooLarge = errors.New("本文が長すぎます")

// 配る側が受け取る ECM の上限 (バイト)。カードに渡す APDU の Lc が 1 バイトなので 255 です。
const MaxECM = 255

// 契約確認データの上限 (バイト)。前に MJD の 2 バイトを付けて、APDU の Lc の 1 バイトに収めます。
const MaxVerification = 253

// CheckConfirm は契約確認データの長さを確かめます (1〜253 バイト)。
func CheckConfirm(verification []byte) error {
	if len(verification) < 1 || len(verification) > MaxVerification {
		return fmt.Errorf("契約確認データの長さがおかしいです (%d バイト)", len(verification))
	}
	return nil
}

// ReadUpTo は本文を limit バイトまで読みます。超えたら、その時点で読むのをやめて
// (nil, ErrTooLarge) を返します。先に全部読むと、LAN の相手が上限の何倍もの本文を溜めさせられます。
func ReadUpTo(body io.Reader, limit int) ([]byte, error) {
	buf := make([]byte, limit+1)
	n := 0
	for n < len(buf) {
		got, err := body.Read(buf[n:])
		n += got
		if err != nil {
			if err == io.EOF {
				return buf[:n], nil
			}
			return nil, err
		}
	}
	return nil, ErrTooLarge
}

// PackInit はカードの定数をバイト列にします。
// 32 バイトの system_key + 8 バイトの init_cbc + ca_system_id (BE32) + カード番号 (BE64 の並び)。
func PackInit(info b25.Info) []byte {
	body := make([]byte, 44+len(info.IDs)*8)
	copy(body[0:], info.SystemKey[:])
	copy(body[32:], info.CBCInit[:])
	binary.BigEndian.PutUint32(body[40:], uint32(info.CASystemID))
	for i, id := range info.IDs {
		binary.BigEndian.PutUint64(body[44+i*8:], cardIDToUint64(id))
	}
	return body
}

// ReadInit はカードの定数をバイト列から読みます。
func ReadInit(body []byte) (b25.Info, error) {
	var info b25.Info
	if len(body) < 44 {
		return info, fmt.Errorf("カードの情報が短すぎます")
	}
	if (len(body)-44)%8 != 0 {
		return info, fmt.Errorf("カードの情報の長さがおかしいです (%d バイト)", len(body))
	}
	copy(info.SystemKey[:], body[0:32])
	copy(info.CBCInit[:], body[32:40])
	ca := binary.BigEndian.Uint32(body[40:44])
	if ca > 0xFFFF {
		return info, fmt.Errorf("CA_system_id が大きすぎます (0x%08x)", ca)
	}
	info.CASystemID = uint16(ca)
	n := (len(body) - 44) / 8
	info.IDs = make([]b25.CardID, n)
	for i := range n {
		info.IDs[i] = uint64ToCardID(binary.BigEndian.Uint64(body[44+i*8:]))
	}
	return info, nil
}

// PackECM は鍵の応答をバイト列にします。奇数鍵 8 バイト + 偶数鍵 8 バイト + 返り値 (BE16)。
func PackECM(answer b25.ECMResult) []byte {
	body := make([]byte, 18)
	copy(body[0:], answer.Odd[:])
	copy(body[8:], answer.Even[:])
	binary.BigEndian.PutUint16(body[16:], answer.Code)
	return body
}

// ReadECM は鍵の応答をバイト列から読みます。
func ReadECM(body []byte) (b25.ECMResult, error) {
	var answer b25.ECMResult
	if len(body) < 18 {
		return answer, fmt.Errorf("鍵の応答が短すぎます")
	}
	copy(answer.Odd[:], body[0:8])
	copy(answer.Even[:], body[8:16])
	answer.Code = binary.BigEndian.Uint16(body[16:18])
	return answer, nil
}

// PackCode は返り値コード 1 つをバイト列にします (BE16)。契約確認と EMM の応答です。
func PackCode(code uint16) []byte {
	body := make([]byte, 2)
	binary.BigEndian.PutUint16(body, code)
	return body
}

// ReadCode は返り値コード 1 つをバイト列から読みます。
func ReadCode(body []byte, what string) (uint16, error) {
	if len(body) < 2 {
		return 0, fmt.Errorf("%sの応答が短すぎます", what)
	}
	return binary.BigEndian.Uint16(body), nil
}

// cardIDToUint64 はカード番号 (6 バイト) を数にします。ParseIDI と同じ並びです。
func cardIDToUint64(id b25.CardID) uint64 {
	var v uint64
	for _, b := range id {
		v = v<<8 | uint64(b)
	}
	return v
}

// uint64ToCardID は数をカード番号 (6 バイト) にします。上の 2 バイトは捨てます。
// wire のカード番号は 48 ビットの数を BE64 にしたものなので、上の 2 バイトは 0 です。
func uint64ToCardID(v uint64) b25.CardID {
	var id b25.CardID
	for i := 5; i >= 0; i-- {
		id[i] = byte(v)
		v >>= 8
	}
	return id
}
