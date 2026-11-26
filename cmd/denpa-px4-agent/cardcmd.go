package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/app"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/card"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/config"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/logx"
)

// cardCmd は `--card` (仕様書 15)。カードリーダーを並べ、カードを開いて INT と IDI の結果を出して終わる。
//
// カードは排他で開くので、**サーバが動いていると開けない** (共有違反)。止めてから使う。
// 結果は標準出力、記録は標準エラー。1 つでも読めれば 0、読めるカードが無ければ 1。
// 出るカード ID はカードの個人情報なので、貼り付けるときは気をつける。
func cardCmd() int {
	exe, _ := os.Executable()
	cfg, err := config.Load(os.Getenv, runtime.GOOS, exe)
	if err != nil {
		fmt.Println(err)
		return 2
	}
	m := app.NewCardManager(cfg, logx.New(os.Stderr))
	reps, err := m.Probe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "カードリーダーを見つけられません: %v\n", err)
		return 1
	}
	if printCard(os.Stdout, reps) == 0 {
		return 1
	}
	return 0
}

// printCard はリーダーごとの結果を書いて、読めたカードの数を返す。
func printCard(w io.Writer, reps []card.ReaderReport) int {
	ok := 0
	fmt.Fprintf(w, "カードリーダー %d 個\n", len(reps))
	for _, r := range reps {
		kind := "外付け"
		if r.Internal {
			kind = "内蔵"
		}
		fmt.Fprintf(w, "  [%s] %s\n", kind, r.Name)
		if r.Err != "" {
			fmt.Fprintf(w, "      読めません: %s\n", r.Err)
			continue
		}
		ok++
		ids := "(IDI で返ってきませんでした)"
		if len(r.IDs) > 0 {
			ids = strings.Join(r.IDs, ", ")
		}
		fmt.Fprintf(w, "      %s  CA_system_id 0x%04X  カード ID %s\n", r.Card, r.CASystemID, ids)
	}
	return ok
}
