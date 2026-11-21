//go:build !linux

package listen

import "net"

// accept4 の代替が要るのは Linux の古いカーネルだけ。
func Wrap(ln net.Listener) net.Listener { return ln }
