package cardshare

import (
	"net"
	"testing"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/config"
)

func tcpAddr(ip, zone string) net.Addr {
	return &net.TCPAddr{IP: net.ParseIP(ip), Port: 1234, Zone: zone}
}

func TestPolicyAllowed(t *testing.T) {
	nets, err := config.ParseShareNetworks(config.DefaultShareNetworks())
	if err != nil {
		t.Fatal(err)
	}
	policy := NewPolicy(nets)
	allowed := []net.Addr{
		tcpAddr("127.0.0.1", ""), tcpAddr("127.1.2.3", ""),
		tcpAddr("10.0.0.5", ""), tcpAddr("172.16.5.5", ""), tcpAddr("172.31.255.255", ""),
		tcpAddr("192.168.1.2", ""),
		tcpAddr("::1", ""), tcpAddr("fc00::1", ""), tcpAddr("fe80::1", ""),
		tcpAddr("fe80::1", "eth0"),        // リンクローカルのゾーン付きも通ります。
		tcpAddr("::ffff:192.168.1.2", ""), // IPv4 射影アドレスは IPv4 として見ます。
	}
	for _, addr := range allowed {
		if !policy.Allowed(addr) {
			t.Fatalf("%s を配る範囲に入れませんでした", addr)
		}
	}
	denied := []net.Addr{
		tcpAddr("172.32.0.1", ""), tcpAddr("8.8.8.8", ""), tcpAddr("203.0.113.5", ""),
		tcpAddr("100.64.0.1", ""), // CGNAT は入れません。
		tcpAddr("::ffff:8.8.8.8", ""),
		tcpAddr("2001:db8::1", ""),
		&net.TCPAddr{IP: nil, Port: 1234}, // IP が読めないものは断ります。
	}
	for _, addr := range denied {
		if policy.Allowed(addr) {
			t.Fatalf("%s を配る範囲に入れてしまいました", addr)
		}
	}
}

// TestAllowedTypedNil は、型つきの nil を渡しても panic しないことを確かめます。
// (*net.TCPAddr)(nil) は、interface としては nil ではないので、
// .IP をそのまま読むと panic します。conn.RemoteAddr() がこの形を返すことは
// 通常ありませんが、「鍵を配ってよいか」の判定で panic しないようにします。
func TestAllowedTypedNil(t *testing.T) {
	nets, err := config.ParseShareNetworks(config.DefaultShareNetworks())
	if err != nil {
		t.Fatal(err)
	}
	policy := NewPolicy(nets)
	for _, addr := range []net.Addr{
		(*net.TCPAddr)(nil),
		(*net.UDPAddr)(nil),
		(*net.IPAddr)(nil),
	} {
		if policy.Allowed(addr) {
			t.Fatalf("%T の nil を配る範囲に入れてしまいました", addr)
		}
	}
}
