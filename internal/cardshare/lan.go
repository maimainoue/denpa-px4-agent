package cardshare

import (
	"net"
)

// Policy は鍵を配る相手の決まりです。アドレスの範囲で絞ります。
type Policy struct {
	nets []*net.IPNet
}

// NewPolicy は確かめた範囲の並びから決まりを作ります。
// config.ParseShareNetworks の結果を渡します。解釈はあちらに 1 か所だけです。
func NewPolicy(nets []*net.IPNet) *Policy {
	return &Policy{nets: nets}
}

// Allowed は相手のアドレスに鍵を配ってよいかです。
// IPv4 射影アドレス (::ffff:192.168.1.2) は IPv4 として見ます。
// *net.TCPAddr の IP を直接使うので、リンクローカルのゾーン (%eth0) は入りません。
func (p *Policy) Allowed(addr net.Addr) bool {
	ip := addrIP(addr)
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

// addrIP は相手のアドレスから IP だけを取ります。
// 知らない種類のアドレスは nil (配らない) です。
func addrIP(addr net.Addr) net.IP {
	switch a := addr.(type) {
	case *net.TCPAddr:
		return a.IP
	case *net.UDPAddr:
		return a.IP
	case *net.IPAddr:
		return a.IP
	default:
		return nil
	}
}
