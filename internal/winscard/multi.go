package winscard

// splitMultiUTF8 は NUL 区切り・NUL 2 つで終わる UTF-8 の文字列の並び (SCardListReaders の返事) を分ける。
// 空の要素は飛ばす。
func splitMultiUTF8(b []byte) []string {
	var out []string
	start := 0
	for i, c := range b {
		if c != 0 {
			continue
		}
		if i > start {
			out = append(out, string(b[start:i]))
		}
		start = i + 1
	}
	return out
}
