// denpa-px4-agent は denpa のチューナーエージェントと同じ API を持ち、px4_drv の機材で選局する。
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/app"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/config"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/logx"
)

// version は -ldflags "-X main.version=…" で入れる。
var version = "dev"

const usage = `使い方: denpa-px4-agent [--version | --pair | --status | --devices | --card | --decode-file <in.ts> [<out.ts>]]
  (無し)     サーバとして動く
  --version  バージョンを出して終わる
  --pair     組むときの URL を標準出力に出して終わる
  --status   動いているエージェントの /denpa/tuners を出して終わる (繋がらなければ 1)
  --devices  機材の定義と、いま見つかっている機材を並べて終わる
  --card     カードリーダーを並べ、カードを開いて INT と IDI の結果を出す (サーバを止めて)
  --decode-file <in.ts> [<out.ts>]  ファイルを復号する (out が無ければ、スクランブルの数を出すだけ)
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) > 0 && args[0] == "--decode-file" {
		return decodeFile(args[1:])
	}
	if len(args) == 1 && args[0] == "--devices" {
		return devicesCmd()
	}
	if len(args) == 1 && args[0] == "--card" {
		return cardCmd()
	}
	if len(args) > 1 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	mode := ""
	if len(args) == 1 {
		mode = args[0]
	}
	switch mode {
	case "", "--pair", "--status":
	case "--version":
		fmt.Println(version)
		return 0
	default:
		fmt.Fprint(os.Stderr, usage)
		return 2
	}

	exe, _ := os.Executable()
	cfg, err := config.Load(os.Getenv, runtime.GOOS, exe)
	if err != nil {
		fmt.Println(err)
		return 2
	}
	if mode == "--status" {
		return status(cfg)
	}
	log := logx.New(os.Stdout)
	a, err := app.New(cfg, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if mode == "--pair" {
		fmt.Println(a.PairURL())
		return 0
	}

	ln, err := a.Listen()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// 止まれの合図は 1 回目で畳み始め (録画が終わるまで待つ)、2 回目は待たずに終わる (仕様書 13)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		cancel()
		<-sig
		log.Printf("2 回目の止まれの合図を受けたので、待たずに終わります")
		os.Exit(1)
	}()

	log.Printf("起動しました (%s, %s)", runtime.GOOS+"/"+runtime.GOARCH, ln.Addr())
	if !a.ID.HasClients() {
		log.Console("denpa と組むときの URL: %s", a.PairURL())
	}
	if err := a.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := a.Serve(ctx, ln); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
