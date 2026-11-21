// aeadbench は ChaCha20-Poly1305 (golang.org/x/crypto/chacha20poly1305) の Seal と Open を測る (イシュー #3)。
//
// 鍵と nonce は固定。長さ 0〜300 の往復と暗号文のチェックサム (ctsum) を出すので、
// 実装を差し替えたとき (例: golang/crypto PR 294) に ctsum が同じかで暗号文の一致を見られる。
// 続けて 1316 B と 16 KiB の Seal・Open を CPU 秒あたりの Mbps で 3 回出す。
// 使い方は README.md。
package main

import (
	"bytes"
	"fmt"
	"os"
	"syscall"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
)

func cpu() time.Duration {
	var r syscall.Rusage
	syscall.Getrusage(0, &r)
	return time.Duration(r.Utime.Nano()) + time.Duration(r.Stime.Nano())
}

func main() {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i*13 + 5)
	}
	a, _ := chacha20poly1305.New(key)
	nonce := make([]byte, 12)
	ad := []byte("hdr13bytes___")
	// 正しさ: 小さい長さを全部、Seal→Open で戻ること + 固定鍵の出力ハッシュを出す
	var sum uint32
	for n := 0; n <= 300; n++ {
		pt := bytes.Repeat([]byte{byte(n)}, n)
		for i := range pt {
			pt[i] ^= byte(i * 7)
		}
		ct := a.Seal(nil, nonce, pt, ad)
		back, err := a.Open(nil, nonce, ct, ad)
		if err != nil || !bytes.Equal(back, pt) {
			fmt.Println("MISMATCH len", n, err)
			os.Exit(1)
		}
		for _, b := range ct {
			sum = sum*31 + uint32(b)
		}
	}
	fmt.Printf("roundtrip ok, ctsum=%08x\n", sum)
	for _, sz := range []int{1316, 16384} {
		buf := make([]byte, sz)
		out := make([]byte, 0, sz+16)
		total := 4 << 20
		for rep := 0; rep < 3; rep++ {
			c0 := cpu()
			for n := 0; n < total; n += sz {
				out = a.Seal(out[:0], nonce, buf, ad)
			}
			dc := cpu() - c0
			c1 := cpu()
			for n := 0; n < total; n += sz {
				a.Open(buf[:0], nonce, out, ad)
			}
			do := cpu() - c1
			fmt.Printf("size=%5d Seal %.1f Mbps/cpu-s  Open %.1f Mbps/cpu-s\n", sz, float64(total)*8/dc.Seconds()/1e6, float64(total)*8/do.Seconds()/1e6)
		}
	}
}
