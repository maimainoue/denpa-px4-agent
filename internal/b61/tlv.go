// Package b61 は 4K 放送 (MMT/TLV) の限定受信を復号する (docs/b61.md)。
// TLV から MMTP を取り出し、MMT-SI の ECM をカード (ARIB STD-B61 の A-CAS) に渡して、MFU のペイロードを AES-128-CTR で復号する。
package b61

import "bytes"

// TLV パケットの型 (docs/b61.md の 4)。
const (
	tlvSync       = 0x7F
	tlvIPv6       = 0x02
	tlvCompressed = 0x03
	tlvSI         = 0xFE
	tlvNull       = 0xFF
)

// 圧縮 IP の CID ヘッダー型。MMTP が本体のどこから始まるかが決まる。
const (
	hdrFull    = 0x60 // IPv6 (長さ抜き 38 バイト) と UDP (ポート 4 バイト)
	hdrPayload = 0x61 // ペイロードだけ
)

const (
	hdrFullOffset    = 3 + 38 + 4
	hdrPayloadOffset = 3
)

func validTLVType(t byte) bool {
	return t == tlvIPv6 || t == tlvCompressed || t == tlvSI || t == tlvNull
}

// nextSync は b[from:] の中で次の同期バイトの位置。無ければ len(b)。
func nextSync(b []byte, from int) int {
	if from >= len(b) {
		return len(b)
	}
	i := bytes.IndexByte(b[from:], tlvSync)
	if i < 0 {
		return len(b)
	}
	return from + i
}

// mmtpOffset は圧縮 IP の本体 (型 0x03) の中の MMTP の位置。扱えないものは -1。
func mmtpOffset(body []byte) int {
	if len(body) < 3 {
		return -1
	}
	switch body[2] {
	case hdrFull:
		if len(body) < hdrFullOffset {
			return -1
		}
		return hdrFullOffset
	case hdrPayload:
		return hdrPayloadOffset
	}
	return -1
}
