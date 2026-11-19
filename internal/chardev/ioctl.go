// Package chardev は Linux の px4_drv のデバイスファイルのバックエンド (仕様書 4 章)。
// ioctl の値と C/N の式、機材の見つけ方は OS に依らず書き、デバイスを開く部分だけ Linux 専用にする。
package chardev

// ioctl の番号。px4_drv の include/ptx_ioctl.h (64 ビットの Linux で _IOC の並びは共通)。
const (
	iocNone  = 0
	iocWrite = 1
	iocRead  = 2
)

func ioc(dir, typ, nr, size uintptr) uintptr {
	return dir<<30 | size<<16 | typ<<8 | nr
}

var (
	ptxSetSystemMode   = ioc(iocWrite, 0x8d, 0x0b, 4) // int
	ptxSetChannel      = ioc(iocWrite, 0x8d, 0x01, 8) // struct ptx_freq { int freq_no; int slot; }
	ptxStartStreaming  = ioc(iocNone, 0x8d, 0x02, 0)
	ptxStopStreaming   = ioc(iocNone, 0x8d, 0x03, 0)
	ptxGetCnr          = ioc(iocRead, 0x8d, 0x04, 8) // int * (64 ビット)
	ptxEnableLnbPower  = ioc(iocWrite, 0x8d, 0x05, 4)
	ptxDisableLnbPower = ioc(iocNone, 0x8d, 0x06, 0)

	// 拡張の ioctl (ISDB-C の TSMF の 1 本目以降と J.83B の選局)
	ptxtSetParams = ioc(iocWrite, 0xe7, 0x02, 24) // struct ptxt_params (24 バイト)
	ptxtTune      = ioc(iocNone, 0xe7, 0x04, 0)
)

// 方式の値 (PTX_SET_SYSTEM_MODE)。
const (
	modeISDBT = 0x10
	modeISDBS = 0x20
	modeISDBC = 0x40
)

// ptxt_params と ptxt_additional_param (px4_drv の include/ptx_ioctl.h)。64 ビットのプロセスから渡す。
type ptxtParams struct {
	System   uint32
	Freq     uint32 // 中心周波数 (kHz)
	NumProp  uint32
	Reserved uint32 // 0
	Prop     uint64 // ptxtProp の配列のアドレス
}

type ptxtProp struct {
	Code uint32
	Data uint32
}

const ptxtTSMFStreamParam = 2 // PTXT_TSMF_STREAM_PARAM

// tsmfProps は ISDB-C の追加パラメーター (TSMF の k 本目)。ISDB-C 以外は無し。
func tsmfProps(system uint32, stream int) []ptxtProp {
	if system != 0x40 {
		return nil
	}
	return []ptxtProp{{Code: ptxtTSMFStreamParam, Data: uint32(stream)}}
}
