package config

import "testing"

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
