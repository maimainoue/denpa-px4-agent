package cmdbackend

import (
	"strconv"
	"strings"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/devices"
)

// Params は command の `{…}` に入れる値。
type Params struct {
	Device  string   // tuners.json の device の `cmd:<model>:` より後ろ
	Type    string   // GR / BS / …
	Channel []string // FormatChannel の結果
	Lnb     string   // "15v" / "11v" / ""
	Service int      // 0 は指定なし
	TSID    int      // 衛星の `:TSID`。無ければ -1
}

// BuildArgs は command を引数の配列に展開する (仕様書 6.1・6.4)。シェルは通さない。
//
//   - `{channel}` だけの要素は引数が 2 つ以上に展開される
//   - `{lnb}` だけの要素は lnbArg の引数に展開される (空なら消える)
//   - `{service}`・`{sidQuery}`・`{tsid}` は値が無ければ空。要素がそれだけなら消える
//   - 局があれば serviceArg を、TSID があれば tsidArg を、末尾に足す
func BuildArgs(def *devices.Def, p Params) []string {
	service, sidQuery, tsid := "", "", ""
	if p.Service != 0 {
		service = strconv.Itoa(p.Service)
		sidQuery = "?sid=" + service
	}
	if p.TSID >= 0 {
		tsid = strconv.Itoa(p.TSID)
	}
	lnbArgs := def.LnbArg[p.Lnb]

	scalar := map[string]string{
		"{device}":   p.Device,
		"{type}":     p.Type,
		"{service}":  service,
		"{sidQuery}": sidQuery,
		"{tsid}":     tsid,
	}
	repl := strings.NewReplacer(
		"{device}", p.Device,
		"{type}", p.Type,
		"{service}", service,
		"{sidQuery}", sidQuery,
		"{tsid}", tsid,
		"{channel}", strings.Join(p.Channel, " "),
		"{lnb}", strings.Join(lnbArgs, " "),
	)

	template := def.Command
	if def.IsHTTP() {
		template = []string{def.URL} // 展開した URL が 1 要素で返る
	}
	var out []string
	for _, a := range template {
		switch a {
		case "{channel}":
			out = append(out, p.Channel...)
			continue
		case "{lnb}":
			out = append(out, lnbArgs...)
			continue
		}
		if v, ok := scalar[a]; ok {
			if v != "" {
				out = append(out, v)
			}
			continue
		}
		out = append(out, repl.Replace(a))
	}

	appendExtra := func(extra []string) {
		for _, a := range extra {
			if v, ok := scalar[a]; ok {
				if v != "" {
					out = append(out, v)
				}
				continue
			}
			out = append(out, repl.Replace(a))
		}
	}
	if p.Service != 0 {
		appendExtra(def.ServiceArg)
	}
	if p.TSID >= 0 {
		appendExtra(def.TsidArg)
	}
	return out
}
