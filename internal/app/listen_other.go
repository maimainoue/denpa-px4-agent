//go:build !linux

package app

import "net"

// accept4 の代替が要るのは Linux の古いカーネルだけ。
func wrapListener(ln net.Listener) net.Listener { return ln }
