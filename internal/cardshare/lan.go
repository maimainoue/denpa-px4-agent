package cardshare

import (
	"net"
	"strings"
)

// Policy は鍵を配る相手の決まりです。アドレスの範囲で絞ります。
type Policy struct {
	nets []*net.IPNet
}

// NewPolicy は CARD_SHARE_NETWORKS の文字列から決まりを作ります。
// config.ParseShareNetworks で確かめた文字列を渡します。
func NewPolicy(networks string) (*Policy, error) {
	var nets []*net.IPNet
	for _, part := range strings.Split(networks, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "/") {
			_, n, err := net.ParseCIDR(part)
			if err != nil {
				return nil, err
			}
			nets = append(nets, n)
		} else {
			ip := net.ParseIP(part)
			if ip == nil {
				return nil, &net.ParseError{Type: "IP address", Text: part}
			}
			bits := 128
			if ip4 := ip.To4(); ip4 != nil {
				ip = ip4
				bits = 32
			}
			nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
		}
	}
	return &Policy{nets: nets}, nil
}

// Allowed は相手のアドレス (host:port) に鍵を配ってよいかです。
// IPv4 射影アドレス (::ffff:192.168.1.2) は IPv4 として見ます。
func (p *Policy) Allowed(addr string) bool {
	ip := peerIP(addr)
	if ip == nil {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	for _, n := range p.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// peerIP は相手のアドレス (host:port) から IP だけを取ります。
func peerIP(addr string) net.IP {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return net.ParseIP(host)
}
