package cardshare

import "errors"

// errTTLNotSet は TTL が付かなかったときの理由です。
var errTTLNotSet = errors.New("TTL が付きませんでした")
