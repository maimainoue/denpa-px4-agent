//go:build !linux && !darwin && !windows

package cardshare

import "syscall"

// この OS では TTL を付けられません。付けるのは諦めて、理由を返します。
// 待ち受けを作るときは失敗する (配らない)、繋ぐときは失敗する (繋がない) ので、
// TTL なしで LAN の外に届くことはありません。
func setSocketTTL(c syscall.RawConn, network string, ttl int) error {
	return errTTLNotSet
}

// getSocketTTL はこの OS では TTL を読めません。
func getSocketTTL(c syscall.RawConn, network string) (int, error) {
	return 0, errTTLNotSet
}

// checkSocketTTL はこの OS では TTL を確かめられません。
func checkSocketTTL(c syscall.RawConn, network string, ttl int) error {
	return errTTLNotSet
}
