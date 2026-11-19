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

const usage = `使い方: denpa-px4-agent [--version | --pair]
  (無し)     サーバとして動く
  --version  バージョンを出して終わる
  --pair     組むときの URL を標準出力に出して終わる
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) > 1 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	mode := ""
	if len(args) == 1 {
		mode = args[0]
	}
	switch mode {
	case "", "--pair":
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
		fmt.Fprintln(os.Stderr, err)
		return 2
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("起動しました (%s, %s)", runtime.GOOS+"/"+runtime.GOARCH, ln.Addr())
	if !a.ID.HasClients() {
		log.Console("denpa と組むときの URL: %s", a.PairURL())
	}
	a.Start(ctx)
	if err := a.Serve(ctx, ln); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
