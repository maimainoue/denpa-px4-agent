package cardshare

import (
	"testing"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/config"
)

func TestPolicyAllowed(t *testing.T) {
	policy, err := NewPolicy(config.DefaultShareNetworks())
	if err != nil {
		t.Fatal(err)
	}
	allowed := []string{
		"127.0.0.1:1234", "127.1.2.3:1234",
		"10.0.0.5:1234", "172.16.5.5:1234", "172.31.255.255:1234",
		"192.168.1.2:1234",
		"[::1]:1234", "[fc00::1]:1234", "[fe80::1]:1234",
		"[::ffff:192.168.1.2]:1234", // IPv4 射影アドレスは IPv4 として見ます。
	}
	for _, addr := range allowed {
		if !policy.Allowed(addr) {
			t.Fatalf("%s を配る範囲に入れませんでした", addr)
		}
	}
	denied := []string{
		"172.32.0.1:1234", "8.8.8.8:1234", "203.0.113.5:1234",
		"100.64.0.1:1234", // CGNAT は入れません。
		"[::ffff:8.8.8.8]:1234",
		"[2001:db8::1]:1234",
		"not-an-ip:1234",
	}
	for _, addr := range denied {
		if policy.Allowed(addr) {
			t.Fatalf("%s を配る範囲に入れてしまいました", addr)
		}
	}
}
