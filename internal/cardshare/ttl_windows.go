//go:build windows

package cardshare

import (
	"strings"
	"syscall"
)

// setSocketTTL はソケットに TTL (IPv4) か hop limit (IPv6) を付けます。
// Windows の SetsockoptInt は Handle を取ります。
func setSocketTTL(c syscall.RawConn, network string, ttl int) error {
	var opErr error
	err := c.Control(func(fd uintptr) {
		handle := syscall.Handle(fd)
		if strings.HasSuffix(network, "6") {
			opErr = syscall.SetsockoptInt(handle, syscall.IPPROTO_IPV6, syscall.IPV6_UNICAST_HOPS, ttl)
		} else {
			opErr = syscall.SetsockoptInt(handle, syscall.IPPROTO_IP, syscall.IP_TTL, ttl)
		}
	})
	if err != nil {
		return err
	}
	return opErr
}

// getSocketTTL はソケットの TTL (IPv4) か hop limit (IPv6) を読みます。
func getSocketTTL(c syscall.RawConn, network string) (int, error) {
	var value int
	var opErr error
	err := c.Control(func(fd uintptr) {
		handle := syscall.Handle(fd)
		if strings.HasSuffix(network, "6") {
			value, opErr = syscall.GetsockoptInt(handle, syscall.IPPROTO_IPV6, syscall.IPV6_UNICAST_HOPS)
		} else {
			value, opErr = syscall.GetsockoptInt(handle, syscall.IPPROTO_IP, syscall.IP_TTL)
		}
	})
	if err != nil {
		return 0, err
	}
	if opErr != nil {
		return 0, opErr
	}
	return value, nil
}

// checkSocketTTL は TTL が付いているか確かめます。付いていなければ理由を返します。
func checkSocketTTL(c syscall.RawConn, network string, ttl int) error {
	got, err := getSocketTTL(c, network)
	if err != nil {
		return err
	}
	if got != ttl {
		return errTTLNotSet
	}
	return nil
}
