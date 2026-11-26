package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// テストの実行ファイル自身を、偽のチューナー (FAKE_TUNE) の子プロセスにする。
//
//	<exe> <種別> <チャンネル>  → T99 なら何も流さず 1 で終わる。それ以外は 188 バイトの TS を流し続ける
const tuneHelperEnv = "DENPA_TUNE_TEST_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(tuneHelperEnv) == "1" {
		tuneHelper(os.Args[1:])
		return
	}
	os.Exit(m.Run())
}

func tuneHelper(args []string) {
	if len(args) >= 2 && args[1] == "T99" {
		os.Stderr.WriteString("Cannot tune to T99: no signal\n")
		os.Exit(1)
	}
	var cc byte
	pkt := make([]byte, 188)
	for {
		for i := 0; i < 50; i++ {
			pkt[0], pkt[1], pkt[2], pkt[3] = 0x47, 0x01, 0x00, 0x10|cc&0x0F // PID 0x100、ペイロードあり、スクランブル無し
			cc++
			if _, err := os.Stdout.Write(pkt); err != nil {
				os.Exit(0)
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestParseTuneArgs(t *testing.T) {
	ok, err := parseTuneArgs([]string{"px4host:{D3C0AEC3-0FC9-4769-8513-8323CEFD792F}:0:0", "T27, T22", "--seconds", "5", "--decode", "--lnb", "15V", "--card-url", "http://192.168.0.10:25261"})
	if err != nil {
		t.Fatal(err)
	}
	if ok.device != "px4host:{D3C0AEC3-0FC9-4769-8513-8323CEFD792F}:0:0" || len(ok.channels) != 2 || ok.channels[0] != "T27" || ok.channels[1] != "T22" ||
		ok.seconds != 5 || !ok.decode || ok.lnb != "15v" || ok.cardURL != "http://192.168.0.10:25261" {
		t.Errorf("%+v", ok)
	}
	// フラグは位置の前にも書ける。既定は 10 秒
	d, err := parseTuneArgs([]string{"--decode", "px4:/dev/px4video2", "BS15"})
	if err != nil || d.seconds != 10 || !d.decode || d.device != "px4:/dev/px4video2" || len(d.channels) != 1 {
		t.Errorf("%+v %v", d, err)
	}
	// 範囲外は、機種が見る (cmd は選局コマンドに任せる)。書式だけ見る
	if _, err := parseTuneArgs([]string{"cmd:x:y", "T99"}); err != nil {
		t.Errorf("T99 は書式が正しい: %v", err)
	}

	for name, args := range map[string][]string{
		"引数なし":          nil,
		"channel が無い":   {"px4:/dev/px4video2"},
		"位置が多い":         {"a", "T27", "b"},
		"channel が空":    {"px4:/dev/px4video2", ", ,"},
		"書式が違うチャンネル":    {"px4:/dev/px4video2", "T27,X99"},
		"seconds が 0":   {"a", "T27", "--seconds", "0"},
		"seconds が大きすぎ": {"a", "T27", "--seconds", "3601"},
		"seconds が数でない": {"a", "T27", "--seconds", "x"},
		"seconds の値が無い": {"a", "T27", "--seconds"},
		"lnb が違う":       {"a", "T27", "--lnb", "9v"},
		"card-url が違う":  {"a", "T27", "--card-url", "ftp://x"},
		"知らないフラグ":       {"a", "T27", "--bogus"},
		"複数の機材 (+)":     {"px4host:a:0:0+px4host:a:1:0", "T27"},
	} {
		if _, err := parseTuneArgs(args); err == nil {
			t.Errorf("%s: 通ってしまった (%v)", name, args)
		}
	}
	if o, err := parseTuneArgs([]string{"a", "BS15", "--lnb", "off"}); err != nil || o.lnb != "" {
		t.Errorf("--lnb off: %+v %v", o, err)
	}
}

func TestComma(t *testing.T) {
	for in, want := range map[int64]string{0: "0", 7: "7", 999: "999", 1000: "1,000", 12978580: "12,978,580", -1234: "-1,234"} {
		if got := comma(in); got != want {
			t.Errorf("comma(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestTSCheck(t *testing.T) {
	mk := func(pid uint16, tei bool, tsc byte, afc byte, cc byte) []byte {
		p := make([]byte, 188)
		p[0], p[1], p[2], p[3] = 0x47, byte(pid>>8)&0x1F, byte(pid), tsc<<6|afc<<4|cc&0x0F
		if tei {
			p[1] |= 0x80
		}
		return p
	}
	var stream []byte
	stream = append(stream, 0x00, 0x13, 0x37) // 先頭にごみ (同期を探し直す)
	stream = append(stream, mk(0x100, false, 0, 1, 0)...)
	stream = append(stream, mk(0x100, false, 0, 1, 1)...)
	stream = append(stream, mk(0x100, false, 0, 1, 1)...) // 同じ値は重複で、許される
	stream = append(stream, mk(0x100, false, 0, 1, 5)...) // 不連続
	stream = append(stream, mk(0x101, true, 2, 1, 7)...)  // TEI・スクランブル。PID が違うので、連続性は比べない
	stream = append(stream, mk(0x1FFF, false, 0, 1, 3)...)
	stream = append(stream, mk(0x1FFF, false, 0, 1, 9)...) // ヌルは数えない
	stream = append(stream, mk(0x100, false, 0, 2, 0)...)  // ペイロード無し (アダプテーションだけ) は数えない

	for _, step := range []int{len(stream), 188, 100, 7} { // 塊の切り方に依らない
		c := newTSCheck()
		for i := 0; i < len(stream); i += step {
			end := i + step
			if end > len(stream) {
				end = len(stream)
			}
			c.add(stream[i:end])
		}
		if c.packets != 8 || c.tei != 1 || c.scrambled != 1 || c.syncLost != 1 || c.ccErr != 1 || c.ccChk != 3 {
			t.Errorf("塊 %d: packets=%d tei=%d scrambled=%d syncLost=%d ccErr=%d ccChk=%d", step, c.packets, c.tei, c.scrambled, c.syncLost, c.ccErr, c.ccChk)
		}
	}
}

// runTune は tuneCmd を、標準出力をファイルに差し替えて呼ぶ。
func runTune(t *testing.T, args ...string) (int, []byte) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DENPA_HOME", dir)
	t.Setenv("TUNERS_FILE", dir+"/config/tuners.json")
	t.Setenv("CARD_URL", "")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(tuneHelperEnv, "1")
	t.Setenv("FAKE_TUNE", exe) // ファイルとして存在するので、分けずに 1 つの実行ファイルとして使う
	f, err := os.CreateTemp(dir, "out-*.ts")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = f
	code := tuneCmd(args)
	os.Stdout = old
	f.Close()
	b, _ := os.ReadFile(f.Name())
	return code, b
}

func TestTuneCmd_偽の選局でTSが出る(t *testing.T) {
	code, out := runTune(t, "cmd:fake:x", "T27", "--seconds", "1")
	if code != 0 {
		t.Fatalf("終了コード = %d", code)
	}
	if len(out) < 188*50 || len(out)%188 != 0 || out[0] != 0x47 {
		t.Fatalf("TS が出ていない: %d バイト", len(out))
	}
	// 流れてきた TS に、傷が無い (同期・連続性カウンタ)
	c := newTSCheck()
	c.add(out)
	if c.packets != int64(len(out)/188) || c.syncLost != 0 || c.ccErr != 0 || c.tei != 0 {
		t.Errorf("傷がある: packets=%d syncLost=%d ccErr=%d tei=%d", c.packets, c.syncLost, c.ccErr, c.tei)
	}
}

func TestTuneCmd_decodeはスクランブルが無ければそのまま通す(t *testing.T) {
	// 偽のチューナーは、スクランブルされていない TS だけを流す。--decode しても、成功して、188 バイトごとの TS が出る
	code, out := runTune(t, "cmd:fake:x", "T27", "--seconds", "1", "--decode")
	if code != 0 {
		t.Fatalf("終了コード = %d", code)
	}
	if len(out) < 188*50 || len(out)%188 != 0 || out[0] != 0x47 {
		t.Errorf("復号の出力: %d バイト", len(out))
	}
	c := newTSCheck()
	c.add(out)
	if c.scrambled != 0 || c.syncLost != 0 {
		t.Errorf("scrambled=%d syncLost=%d", c.scrambled, c.syncLost)
	}
}

func TestTuneCmd_複数のチャンネルは1つでも失敗すれば1(t *testing.T) {
	code, out := runTune(t, "cmd:fake:x", "T99,T27", "--seconds", "1")
	if code != 1 {
		t.Errorf("終了コード = %d, want 1", code)
	}
	if len(out) < 188*10 { // 失敗しても、あとのチャンネルは続ける
		t.Errorf("T27 の TS が出ていない: %d バイト", len(out))
	}
	if code, _ := runTune(t, "cmd:fake:x", "T99", "--seconds", "1"); code != 1 {
		t.Errorf("T99 だけ = %d, want 1", code)
	}
}

func TestTuneCmd_引数の誤りは2(t *testing.T) {
	for _, args := range [][]string{{}, {"cmd:fake:x"}, {"cmd:fake:x", "T27", "--seconds", "0"}, {"a+b", "T27"}} {
		if code, _ := runTune(t, args...); code != 2 {
			t.Errorf("%v = %d, want 2", args, code)
		}
	}
	if !strings.Contains(tuneUsage, "--tune") {
		t.Error("使い方に --tune が無い")
	}
}
