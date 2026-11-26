package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/app"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/card"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/channel"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/config"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/logx"
)

const tuneUsage = `使い方: denpa-px4-agent --tune <device> <channel[,channel…]> [--lnb 15v] [--decode] [--card-url <url>] [--seconds N]
  device   px4:/dev/px4video2 (Linux)・px4host:<GUID>:<受信機の番号>:<台目> (Windows・macOS)・cmd:<機種>:<device>
  channel  T27・BS15:16625 など。「,」で並べると 1 つずつ選局する
  --lnb      衛星の給電 (15v か 11v)
  --decode   復号する (カードか --card-url が要る。EMM は渡さない)
  --card-url 鍵を貰う先 (CARD_URL と同じ)
  --seconds  1 つのチャンネルを読む秒数 (最初の TS が来てから。既定 10)
TS は標準出力に出す (ファイルにリダイレクトする)。端末のときは捨てて、測定の結果だけを標準エラーに出す。
`

// tuneOpts は --tune の引数。
type tuneOpts struct {
	device   string
	channels []string
	lnb      string
	decode   bool
	cardURL  string
	seconds  int
}

// parseTuneArgs は --tune のあとの引数を読む。フラグは位置のあとにも前にも書ける。
func parseTuneArgs(args []string) (tuneOpts, error) {
	o := tuneOpts{seconds: 10}
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		value := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s の値がありません", a)
			}
			i++
			return args[i], nil
		}
		switch a {
		case "--decode":
			o.decode = true
		case "--lnb":
			v, err := value()
			if err != nil {
				return o, err
			}
			switch strings.ToLower(v) {
			case "15v", "11v":
				o.lnb = strings.ToLower(v)
			case "0v", "off":
				o.lnb = ""
			default:
				return o, fmt.Errorf("--lnb は 15v か 11v にします (%s)", v)
			}
		case "--card-url":
			v, err := value()
			if err != nil {
				return o, err
			}
			if err := config.ValidateCardURL(v); err != nil {
				return o, err
			}
			o.cardURL = v
		case "--seconds":
			v, err := value()
			if err != nil {
				return o, err
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 3600 {
				return o, fmt.Errorf("--seconds は 1〜3600 の整数にします (%s)", v)
			}
			o.seconds = n
		default:
			if strings.HasPrefix(a, "--") {
				return o, fmt.Errorf("対応していない引数です: %s", a)
			}
			pos = append(pos, a)
		}
	}
	if len(pos) != 2 {
		return o, errors.New("device と channel が要ります")
	}
	o.device = pos[0]
	if strings.Contains(o.device, "+") {
		return o, errors.New("複数の機材 (+) の同時選局は、まだ対応していません。1 台ずつ指定します")
	}
	for _, c := range strings.Split(pos[1], ",") {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		// 範囲は機種が見る (cmd は選局コマンドに任せる)。ここでは書式だけ見る
		if _, err := channel.ParseLax(c); err != nil {
			return o, err
		}
		o.channels = append(o.channels, c)
	}
	if len(o.channels) == 0 {
		return o, errors.New("channel が空です")
	}
	return o, nil
}

// tuneCmd は `--tune` (仕様書 15)。サーバを立てずに 1 台を選局して、TS を標準出力に出す。実機で確かめる入口。
// 測定の結果 (最初の TS までの時間・量・同期・TEI・連続性カウンタ・C/N・復号) は標準エラーに出す。
// 終了コードは、全部のチャンネルで TS が来れば 0、1 つでも来なければ 1、引数の誤りは 2。
func tuneCmd(args []string) int {
	opts, err := parseTuneArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprint(os.Stderr, tuneUsage)
		return 2
	}
	exe, _ := os.Executable()
	cfg, err := config.Load(os.Getenv, runtime.GOOS, exe)
	if err != nil {
		fmt.Println(err)
		return 2
	}
	if opts.cardURL != "" {
		cfg.CardURL = opts.cardURL
	}
	log := logx.New(os.Stderr)

	var sink io.Writer = os.Stdout
	if fi, err := os.Stdout.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		sink = io.Discard
		fmt.Fprintln(os.Stderr, "標準出力が端末なので、TS は捨てて、測定の結果だけを出します (TS が要るときは > ファイル にリダイレクトします)")
	}

	tn, err := app.OpenTuner(cfg, log, backend.TunerSpec{Name: "tune", Device: opts.device, Lnb: opts.lnb})
	if err != nil {
		fmt.Fprintf(os.Stderr, "機材を開けません: %v\n", err)
		return 1
	}
	defer tn.Close()
	var cm *card.Manager
	if opts.decode {
		cm = app.NewCardManager(cfg, log)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ok := true
	for _, ch := range opts.channels {
		if ctx.Err() != nil {
			break
		}
		if !tuneOne(ctx, tn, ch, opts, sink, cm) {
			ok = false
		}
	}
	if !ok {
		return 1
	}
	return 0
}

// firstTSWait は選局してから最初の TS を待つ上限。
const firstTSWait = 30 * time.Second

// tuneOne は 1 つのチャンネルを選局して、最初の TS が来てから opts.seconds 秒読む。成功 (TS が来て、復号も通った) なら true。
func tuneOne(ctx context.Context, tn backend.Tuner, ch string, opts tuneOpts, sink io.Writer, cm *card.Manager) bool {
	c, _ := channel.ParseLax(ch)
	began := time.Now()
	if err := tn.Tune(backend.Tuning{Type: string(c.Type), Channel: c.String(), NeedsKey: opts.decode}); err != nil {
		fmt.Fprintf(os.Stderr, "%s: 選局に失敗しました: %v\n", ch, err)
		tn.Stop()
		return false
	}
	rd, err := tn.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: 読み出しを始められません: %v\n", ch, err)
		tn.Stop()
		return false
	}

	type chunk struct {
		b   []byte
		err error
	}
	chunks := make(chan chunk, 16)
	go func() {
		defer close(chunks)
		buf := make([]byte, 64*1024)
		for {
			n, err := rd.Read(buf)
			if n > 0 {
				chunks <- chunk{b: append([]byte(nil), buf[:n]...)}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					chunks <- chunk{err: err}
				}
				return
			}
		}
	}()

	w := bufio.NewWriterSize(sink, 256*1024)
	var (
		check    = newTSCheck()
		dec      *card.Decoder
		total    int64
		first    time.Duration
		gotFirst bool
		readErr  error
		wErr     error
		tlvWarn  bool
		timeout  = time.NewTimer(firstTSWait)
		deadline <-chan time.Time
	)
	if cm != nil {
		dec = cm.NewDecoder()
	}
	defer timeout.Stop()
loop:
	for {
		select {
		case cc, open := <-chunks:
			if !open {
				break loop
			}
			if cc.err != nil {
				readErr = cc.err
				break loop
			}
			if !gotFirst {
				gotFirst, first = true, time.Since(began)
				timeout.Stop()
				deadline = time.After(time.Duration(opts.seconds) * time.Second)
			}
			total += int64(len(cc.b))
			check.add(cc.b)
			out := cc.b
			if dec != nil {
				if cc.b[0] == 0x7F { // 4K (MMT/TLV)。b25 では復号できない
					tlvWarn = true
				} else {
					out = dec.Write(cc.b)
				}
			}
			if _, err := w.Write(out); err != nil {
				wErr = err
				break loop
			}
		case <-timeout.C:
			break loop
		case <-deadline:
			break loop
		case <-ctx.Done():
			break loop
		}
	}
	read := time.Since(began) - first
	sig, _ := tn.Signal() // 止める前に取る
	tn.Stop()
	rd.Close()
	for range chunks { // 読みの goroutine が終わるまで捨てる
	}
	if dec != nil {
		if _, err := w.Write(dec.Flush()); err != nil && wErr == nil {
			wErr = err
		}
	}
	if err := w.Flush(); err != nil && wErr == nil {
		wErr = err
	}

	ok := true
	if !gotFirst {
		why := "TS が来ませんでした"
		if readErr != nil {
			why = fmt.Sprintf("読めなくなりました (%v)", readErr)
		} else if ctx.Err() != nil {
			why = "止められました"
		}
		fmt.Fprintf(os.Stderr, "%s: %s (%.0f 秒待ちました)\n", ch, why, time.Since(began).Seconds())
		return false
	}
	fmt.Fprintf(os.Stderr, "%s: 最初の TS まで %.1f 秒\n", ch, first.Seconds())
	mbps := 0.0
	if read > 0 {
		mbps = float64(total) * 8 / read.Seconds() / 1e6
	}
	fmt.Fprintf(os.Stderr, "    %.1f 秒で %s バイト (%.1f Mbps)  %s パケット\n", read.Seconds(), comma(total), mbps, comma(check.packets))
	scr := 0.0
	if check.packets > 0 {
		scr = 100 * float64(check.scrambled) / float64(check.packets)
	}
	fmt.Fprintf(os.Stderr, "    同期はずれ %d  TEI %d  連続性カウンタの不連続 %d / %d  スクランブル %s (%.1f%%)\n",
		check.syncLost, check.tei, check.ccErr, check.ccChk, comma(check.scrambled), scr)
	if sig != nil && sig.CN != nil {
		unit := sig.Unit
		if unit == "" {
			unit = "dB"
		}
		fmt.Fprintf(os.Stderr, "    C/N %.2f %s%s\n", *sig.CN, unit, map[bool]string{true: "", false: " (ロックしていない)"}[sig.Locked])
	}
	if tlvWarn {
		fmt.Fprintln(os.Stderr, "    4K (MMT/TLV) は復号できないので、そのまま出しました")
	}
	if dec != nil && !tlvWarn {
		st := dec.Stats()
		fmt.Fprintf(os.Stderr, "    復号した %s / スクランブルされたまま %s パケット\n", comma(st.Decoded()), comma(st.Remaining))
		if st.Failed() {
			fmt.Fprintf(os.Stderr, "    復号できませんでした (%s)\n", cm.DecodeFailReason())
			ok = false
		}
	}
	if wErr != nil {
		fmt.Fprintf(os.Stderr, "    標準出力に書けません: %v\n", wErr)
		ok = false
	}
	return ok
}

// comma は 3 桁ごとに「,」を入れる。
func comma(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// tsCheck は TS の流れの傷を数える。境目の探し方は b25.Stream.Process と同じ。
type tsCheck struct {
	buf       []byte
	packets   int64
	syncLost  int64 // 同期がはずれて、境目を探し直した回数
	tei       int64 // transport_error_indicator
	scrambled int64 // transport_scrambling_control が立っているもの
	ccErr     int64 // 連続性カウンタの不連続
	ccChk     int64 // 連続性カウンタを見た数
	cc        map[uint16]uint8
}

func newTSCheck() *tsCheck { return &tsCheck{cc: map[uint16]uint8{}} }

func (c *tsCheck) add(b []byte) {
	c.buf = append(c.buf, b...)
	pos := 0
	for len(c.buf)-pos >= 188 {
		if c.buf[pos] != 0x47 || (len(c.buf)-pos > 188 && c.buf[pos+188] != 0x47) {
			c.syncLost++
			i := bytes.IndexByte(c.buf[pos+1:], 0x47)
			if i < 0 {
				pos = len(c.buf)
				break
			}
			pos += 1 + i
			continue
		}
		p := c.buf[pos : pos+188]
		c.packets++
		if p[1]&0x80 != 0 {
			c.tei++
		}
		if p[3]&0xC0 != 0 {
			c.scrambled++
		}
		pid := uint16(p[1]&0x1F)<<8 | uint16(p[2])
		afc, cnt := (p[3]>>4)&3, p[3]&0x0F
		if pid != 0x1FFF && afc&1 != 0 { // ヌルと、ペイロードの無いパケットは数えない
			if last, ok := c.cc[pid]; ok {
				c.ccChk++
				if cnt != last && cnt != (last+1)&0x0F { // 同じ値は重複で、許される
					c.ccErr++
				}
			}
			c.cc[pid] = cnt
		}
		pos += 188
	}
	c.buf = append(c.buf[:0], c.buf[pos:]...)
}
