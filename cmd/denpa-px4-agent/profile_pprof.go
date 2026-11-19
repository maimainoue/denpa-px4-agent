//go:build pprof && !windows

package main

import (
	"os"
	"os/signal"
	"runtime/pprof"
	"syscall"
)

// 調査用 (タグ pprof のときだけ入る)。環境変数 CPUPROFILE にファイル名を書くと、起動から SIGUSR1 までの
// CPU プロファイルを書く。nasne の上で、どこで CPU を使っているかを調べるために足した。
func init() {
	path := os.Getenv("CPUPROFILE")
	if path == "" {
		return
	}
	f, err := os.Create(path)
	if err != nil {
		return
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		f.Close()
		return
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	go func() {
		<-ch
		pprof.StopCPUProfile()
		f.Close()
	}()
}
