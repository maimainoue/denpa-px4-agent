//go:build darwin

package cardshare

import (
	"fmt"
	"strings"
	"syscall"
)

// setSocketTTL はソケットに TTL (IPv4) か hop limit (IPv6) を付けます。
func setSocketTTL(c syscall.RawConn, network string, ttl int) error {
	var opErr error
	err := c.Control(func(fd uintptr) {
		if strings.HasSuffix(network, "6") {
			opErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_UNICAST_HOPS, ttl)
		} else {
			opErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TTL, ttl)
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
		if strings.HasSuffix(network, "6") {
			value, opErr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_UNICAST_HOPS)
		} else {
			value, opErr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TTL)
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
		return fmt.Errorf("TTL が読めません (%v)", err)
	}
	if got != ttl {
		return fmt.Errorf("TTL が %d になっていません (%d)", ttl, got)
	}
	return nil
}
