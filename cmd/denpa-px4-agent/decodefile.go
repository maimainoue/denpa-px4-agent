package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/app"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/card"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/config"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/logx"
)

const decodeUsage = "使い方: denpa-px4-agent --decode-file <in.ts> [<out.ts>]\n"

// decodeFile は `--decode-file <in.ts> [<out.ts>]` (仕様書 15)。サーバを立てずにファイルを復号する。
//
//   - out が無ければ復号せず、スクランブルされたパケットの数と割合を出す。カードには触らない
//   - out があれば復号して書く。書き出す先がもうあれば断る (上書きしない)。
//     復号できなかったら (残りが復号できた数の 1% を超えた) 出力を消して 1 を返す。
//     CARD_URL (と CARD_TTL) を書けば、手元のカードの代わりに鍵を貰う
//
// 結果は標準出力、記録 (カードのログなど) は標準エラーに出す。置き場 (RAW_DIR) の制限は受けない (サーバの /denpa/decode とは違う)。
func decodeFile(args []string) int {
	if len(args) < 1 || len(args) > 2 {
		fmt.Fprint(os.Stderr, decodeUsage)
		return 2
	}
	in, out := args[0], ""
	if len(args) == 2 {
		out = args[1]
	}

	src, err := os.Open(in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "入力を開けません: %v\n", err)
		return 1
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		fmt.Fprintf(os.Stderr, "入力を調べられません: %v\n", err)
		return 1
	}

	if out == "" {
		st, err := card.ObserveTS(src)
		if err != nil {
			fmt.Fprintf(os.Stderr, "入力を読めません: %v\n", err)
			return 1
		}
		fmt.Printf("%d バイト、%d パケット  %s\n", info.Size(), st.Packets, scrambledSummary(st))
		return 0
	}

	if same(in, out) {
		fmt.Fprintln(os.Stderr, "入力と出力が同じファイルです")
		return 2
	}
	exe, _ := os.Executable()
	cfg, err := config.Load(os.Getenv, runtime.GOOS, exe)
	if err != nil {
		fmt.Println(err)
		return 2
	}
	// O_EXCL: もうあるファイルは上書きしない
	dst, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "書き出す先を作れません: %v\n", err)
		return 1
	}
	m := app.NewCardManager(cfg, logx.New(os.Stderr))
	st, derr := m.DecodeStream(src, dst)
	if cerr := dst.Close(); derr == nil {
		derr = cerr
	}
	if derr != nil {
		os.Remove(out)
		fmt.Fprintf(os.Stderr, "復号できません: %v\n", derr)
		return 1
	}
	fmt.Printf("%d -> %d バイト  %s\n", info.Size(), st.Bytes, scrambledSummary(card.DecodeStats{Packets: st.Packets, Scrambled: st.Scrambled}))
	fmt.Printf("復号した %d / スクランブルされたまま %d パケット\n", st.Decoded(), st.Remaining)
	if st.Failed() {
		os.Remove(out)
		fmt.Fprintf(os.Stderr, "復号できませんでした (%s)。出力は消しました\n", m.DecodeFailReason())
		return 1
	}
	return 0
}

// scrambledSummary は「元は N パケット中 M がスクランブルされています (x%)」。
func scrambledSummary(st card.DecodeStats) string {
	if st.Packets == 0 {
		return "パケットがありません (188 バイトごとの TS ではありません)"
	}
	return fmt.Sprintf("スクランブルされているのは %d / %d パケット (%.1f%%)", st.Scrambled, st.Packets, 100*float64(st.Scrambled)/float64(st.Packets))
}

// same は同じファイルを指しているか (あれば)。
func same(a, b string) bool {
	if absA, err := filepath.Abs(a); err == nil {
		if absB, err := filepath.Abs(b); err == nil && absA == absB {
			return true
		}
	}
	ia, err1 := os.Stat(a)
	ib, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && !errors.Is(err1, os.ErrNotExist) && os.SameFile(ia, ib)
}
