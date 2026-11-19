package api

import "encoding/base64"

// decodeBase64 は標準と URL 安全のどちらも、パディングの有無も受ける。
func decodeBase64(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, base64.CorruptInputError(0)
}
