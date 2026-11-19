package chardev

import "math"

// CNR は PTX_GET_CNR の生の値を dB にする (仕様書 7.4。PT1/PT3 の表示で使われてきた式)。
// 値が使えなければ ok=false。satellite は ISDB-S (16 ビットの値)、それ以外は 24 ビットの値。
func CNR(raw uint32, satellite bool) (db float64, ok bool) {
	if satellite {
		if raw < 3000 {
			return 0, false
		}
		p := math.Sqrt(float64(raw)-3000) / 64
		p2 := p * p
		return -1.6346*p2*p2*p + 14.341*p2*p2 - 50.259*p2*p + 88.977*p2 - 89.565*p + 58.857, true
	}
	if raw == 0 {
		return 0, false
	}
	p := 10 * math.Log10(5505024/float64(raw))
	p2 := p * p
	return 0.000024*p2*p2 - 0.0016*p2*p + 0.0398*p2 + 0.5491*p + 3.0965, true
}
