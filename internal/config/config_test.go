package config

import "testing"

func TestValidateCardURL(t *testing.T) {
	for _, good := range []string{"http://192.168.0.10:25261", "http://nasne.local:25261"} {
		if err := ValidateCardURL(good); err != nil {
			t.Errorf("%q を断りました: %v", good, err)
		}
	}
	for _, bad := range []string{"https://192.168.0.10:25261", "192.168.0.10:25261", "http://", ""} {
		if err := ValidateCardURL(bad); err == nil {
			t.Errorf("%q を受けました", bad)
		}
	}
}

func TestLoad_CARD_URL(t *testing.T) {
	load := func(v string) (*Config, error) {
		return Load(func(k string) string {
			if k == "CARD_URL" {
				return v
			}
			return ""
		}, "linux", "/usr/local/sbin/denpa-px4-agent")
	}
	c, err := load("")
	if err != nil || c.CardURL != "" {
		t.Fatalf("%+v %v", c, err)
	}
	if c, err = load("http://192.168.0.10:25261"); err != nil || c.CardURL != "http://192.168.0.10:25261" {
		t.Errorf("%+v %v", c, err)
	}
	if _, err := load("https://192.168.0.10:25261"); err == nil {
		t.Error("https を受けました")
	}
}

func TestLoad_STREAM_QUEUE_BYTES(t *testing.T) {
	load := func(v string) (*Config, error) {
		return Load(func(k string) string {
			if k == "STREAM_QUEUE_BYTES" {
				return v
			}
			return ""
		}, "linux", "/usr/local/sbin/denpa-px4-agent")
	}
	c, err := load("")
	if err != nil || c.StreamQueueBytes != 0 {
		t.Fatalf("既定は 0 (manager が 64MB にする): %+v %v", c, err)
	}
	if c, err = load("16777216"); err != nil || c.StreamQueueBytes != 16<<20 {
		t.Errorf("%+v %v", c, err)
	}
	for _, bad := range []string{"abc", "1000", "-5"} {
		if _, err := load(bad); err == nil {
			t.Errorf("%q は断る", bad)
		}
	}
}

func TestLoad_CARD_SHARE_NETWORKS(t *testing.T) {
	load := func(port, networks string) (*Config, error) {
		return Load(func(k string) string {
			switch k {
			case "CARD_SHARE_PORT":
				return port
			case "CARD_SHARE_NETWORKS":
				return networks
			}
			return ""
		}, "linux", "/usr/local/sbin/denpa-px4-agent")
	}
	// 既定は LAN の範囲全部です。
	c, err := load("", "")
	if err != nil || c.CardShareNetworks != DefaultShareNetworks() {
		t.Fatalf("既定が違います: %q %v", c.CardShareNetworks, err)
	}
	// 配らないときは、範囲の検証をしません。
	if _, err := load("", "0.0.0.0/0"); err != nil {
		t.Fatalf("配らないのに範囲で断りました: %v", err)
	}
	// 配るときは、既定の範囲の中だけです。
	if _, err := load("25253", "192.168.1.0/24"); err != nil {
		t.Fatalf("LAN の範囲なのに断りました: %v", err)
	}
	for _, bad := range []string{"0.0.0.0/0", "100.64.0.0/10", "8.8.8.8", "not-an-ip"} {
		if _, err := load("25253", bad); err == nil {
			t.Errorf("%q が通ってしまいました", bad)
		}
	}
}

func TestParseShareNetworks(t *testing.T) {
	// 既定の範囲の中なら、範囲でもアドレス 1 つでも書けます。
	for _, s := range []string{
		"192.168.1.0/24",
		"192.168.1.5",
		"10.0.0.0/8,192.168.0.0/16",
		"::1",
		"fe80::/10",
	} {
		if _, err := ParseShareNetworks(s); err != nil {
			t.Fatalf("%q が読めません: %v", s, err)
		}
	}
	// 既定の範囲からはみ出すものは読めません。
	for _, s := range []string{
		"0.0.0.0/0",
		"100.64.0.0/10",
		"8.8.8.8",
		"169.254.0.0/16",
		"2001:db8::/32",
		"192.168.0.0/15", // 192.168.0.0/16 より広いです。
		"not-an-ip",
		"",
	} {
		if _, err := ParseShareNetworks(s); err == nil {
			t.Fatalf("%q が通ってしまいました", s)
		}
	}
}

func TestLoad_CARD_TTL(t *testing.T) {
	load := func(v string) (*Config, error) {
		return Load(func(k string) string {
			if k == "CARD_TTL" {
				return v
			}
			return ""
		}, "linux", "/usr/local/sbin/denpa-px4-agent")
	}
	c, err := load("")
	if err != nil || c.CardTTL != 1 {
		t.Fatalf("既定は 1: %+v %v", c, err)
	}
	if c, err = load("8"); err != nil || c.CardTTL != 8 {
		t.Errorf("%+v %v", c, err)
	}
	for _, bad := range []string{"0", "9", "abc"} {
		if _, err := load(bad); err == nil {
			t.Errorf("%q を受けました", bad)
		}
	}
}

func TestLoad_CARD_SHARE_MAX_RTT(t *testing.T) {
	load := func(v string) (*Config, error) {
		return Load(func(k string) string {
			if k == "CARD_SHARE_MAX_RTT" {
				return v
			}
			return ""
		}, "linux", "/usr/local/sbin/denpa-px4-agent")
	}
	c, err := load("")
	if err != nil || c.CardShareMaxRTTMs != 0 {
		t.Fatalf("既定は 0 (見ない): %+v %v", c, err)
	}
	if c, err = load("100"); err != nil || c.CardShareMaxRTTMs != 100 {
		t.Errorf("%+v %v", c, err)
	}
	for _, bad := range []string{"-1", "abc"} {
		if _, err := load(bad); err == nil {
			t.Errorf("%q を受けました", bad)
		}
	}
}

func TestLoad_WINSCARD_DLLS(t *testing.T) {
	load := func(v string) (*Config, error) {
		return Load(func(k string) string {
			if k == "WINSCARD_DLLS" {
				return v
			}
			return ""
		}, "windows", `C:\denpa\denpa-px4-agent.exe`)
	}
	c, err := load("")
	if err != nil || len(c.WinscardDLLs) != 0 {
		t.Fatalf("未指定: %+v %v", c, err)
	}
	// 空の要素・前後の空白・大文字小文字だけ違う重複は整える。順番は書いたとおり
	c, err = load(` C:\vendor\a\winscard.dll ;; D:/vendor/b/winscard.dll;c:\VENDOR\A\winscard.dll;\\nas\share\winscard.dll`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`C:\vendor\a\winscard.dll`, `D:/vendor/b/winscard.dll`, `\\nas\share\winscard.dll`}
	if len(c.WinscardDLLs) != len(want) {
		t.Fatalf("got %q, want %q", c.WinscardDLLs, want)
	}
	for i := range want {
		if c.WinscardDLLs[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, c.WinscardDLLs[i], want[i])
		}
	}
	// 相対パス・ドライブなし・ファイル名だけは、探し場所を選べないので受けない
	for _, bad := range []string{`winscard.dll`, `.\winscard.dll`, `\vendor\winscard.dll`, `C:winscard.dll`, `C:\ok.dll;relative.dll`, `/usr/lib/libpcsclite.so`} {
		if _, err := load(bad); err == nil {
			t.Errorf("%q を受けました", bad)
		}
	}
}

func TestParseList_DETECT_INCLUDE_はカンマで分ける(t *testing.T) {
	got := ParseList(" PLEX PX-W3U4 , ,denpa,")
	if len(got) != 2 || got[0] != "PLEX PX-W3U4" || got[1] != "denpa" {
		t.Fatalf("got = %q", got)
	}
	if ParseList("") != nil {
		t.Error("空なら nil のはず")
	}
}
