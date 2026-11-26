package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"text/tabwriter"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/app"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/config"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/devices"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/logx"
)

// devicesCmd は `--devices` (仕様書 15)。機材の定義 (組み込み + devices.json) と、
// いま見つかっている機材と、tuners.json の定義を並べて終わる。機材は開かない。
func devicesCmd() int {
	exe, _ := os.Executable()
	cfg, err := config.Load(os.Getenv, runtime.GOOS, exe)
	if err != nil {
		fmt.Println(err)
		return 2
	}
	// 記録 (devices.json の書き損じなど) は標準エラーに出し、一覧は標準出力に出す
	printDevices(os.Stdout, app.ListDevices(cfg, logx.New(os.Stderr)))
	return 0
}

func printDevices(w io.Writer, rep app.DevicesReport) {
	fmt.Fprintf(w, "機材の定義 (%d 機種)\n", len(rep.Defs))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  機種\t経路\tUSB\t受信機\tLNB\tカード\tDriverHost_PX4 の GUID")
	for _, d := range rep.Defs {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			d.Model, routesOf(d), dash(strings.Join(d.USB, ",")), receiversOf(d), lnbOf(d), cardOf(d), hostGUID(d))
	}
	tw.Flush()

	fmt.Fprintln(w, "\nいま見つかっている機材 (自動検出)")
	switch {
	case !rep.HasDetector:
		fmt.Fprintln(w, "  (この OS には自動検出がありません。tuners.json に px4host:<GUID>:<受信機の番号>:<台目> を書きます。自動検出があるのは Linux の chardev だけです)")
	case len(rep.Detected) == 0 && len(rep.DetectErrs) == 0:
		fmt.Fprintln(w, "  (見つかりません)")
	}
	for _, s := range rep.Detected {
		fmt.Fprintf(w, "  %s  %s  (%s)\n", s.Name, s.Device, strings.Join(s.Types, "/"))
	}
	for _, e := range rep.DetectErrs {
		fmt.Fprintf(w, "  探せません: %s\n", e)
	}

	fmt.Fprintf(w, "\ntuners.json (%s)\n", rep.TunersFile)
	if !rep.Found {
		fmt.Fprintln(w, "  (ファイルがありません。サーバを起動すると、自動検出した機材を使います)")
		return
	}
	if len(rep.Configured) == 0 {
		fmt.Fprintln(w, "  (チューナーがありません)")
		return
	}
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  名前\t機種\t受信機\t種別\tdevice\t備考")
	for i, s := range rep.Configured {
		res := devices.Resolved{Receiver: -1}
		if i < len(rep.Models) {
			res = rep.Models[i]
		}
		var notes []string
		if s.Disabled {
			notes = append(notes, "無効")
		}
		if s.Lnb != "" {
			notes = append(notes, "lnb "+s.Lnb)
		}
		if res.Note != "" {
			notes = append(notes, res.Note)
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\n", s.Name, modelOf(res), receiverOf(res), dash(strings.Join(s.Types, "/")), dash(s.Device), dash(strings.Join(notes, ", ")))
	}
	tw.Flush()
}

// modelOf は機種の表示。1 つに決まれば機種名、決まらなければ候補 (多いときは先頭 3 つと「ほか」)。
func modelOf(r devices.Resolved) string {
	switch {
	case len(r.Models) == 0:
		return "-"
	case r.Exact:
		return r.Models[0]
	case len(r.Models) <= 3:
		return "候補: " + strings.Join(r.Models, " / ")
	default:
		return fmt.Sprintf("候補: %s ほか %d 機種", strings.Join(r.Models[:3], " / "), len(r.Models)-3)
	}
}

func receiverOf(r devices.Resolved) string {
	if r.Receiver < 0 {
		return "-"
	}
	return fmt.Sprintf("#%d", r.Receiver)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// routesOf は機材に触る経路。chardev (Linux)・host (Windows と macOS の DriverHost_PX4)・cmd・HTTP。
func routesOf(d devices.Def) string {
	var r []string
	if d.Linux != nil {
		r = append(r, "chardev")
	}
	if d.Host != nil {
		r = append(r, "host")
	}
	if d.URL != "" {
		r = append(r, "http")
	} else if len(d.Command) > 0 {
		r = append(r, "cmd")
	}
	return dash(strings.Join(r, "+"))
}

// receiversOf は受信機の数と、受けられる種別 (`4 基: BS/CS×2, GR×2`)。
// 同じ種別が続くものは、`×個数` にまとめる。
func receiversOf(d devices.Def) string {
	if len(d.Receivers) == 0 {
		return "-"
	}
	var parts []string
	for i := 0; i < len(d.Receivers); {
		t := strings.Join(d.Receivers[i].Types, "/")
		n := 1
		for i+n < len(d.Receivers) && strings.Join(d.Receivers[i+n].Types, "/") == t {
			n++
		}
		if n > 1 {
			parts = append(parts, fmt.Sprintf("%s×%d", t, n))
		} else {
			parts = append(parts, t)
		}
		i += n
	}
	return fmt.Sprintf("%d 基: %s", len(d.Receivers), strings.Join(parts, ", "))
}

// lnbOf は給電できる電圧。書いていなければ -、給電できなければ「なし」。
func lnbOf(d devices.Def) string {
	if d.LNB == nil {
		return "-"
	}
	if len(d.LNB) == 0 {
		return "なし"
	}
	var v []string
	for _, n := range d.LNB {
		v = append(v, fmt.Sprintf("%dv", n))
	}
	return strings.Join(v, "/")
}

func cardOf(d devices.Def) string {
	if d.CardReader {
		return "内蔵"
	}
	return "-"
}

func hostGUID(d devices.Def) string {
	if d.Host == nil {
		return "-"
	}
	return d.Host.DeviceGUID
}
