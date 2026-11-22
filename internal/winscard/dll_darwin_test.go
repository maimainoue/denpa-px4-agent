//go:build darwin

package winscard

import (
	"errors"
	"testing"
)

func TestLoadDLLMissing(t *testing.T) {
	if _, err := LoadDLL("/nonexistent/libpcsc.dylib"); err == nil {
		t.Error("無いライブラリを読めました")
	}
}

// 本物の PCSC.framework を読んで、リーダーの一覧を取る。
// pcscd (com.apple.ctkpcscd) が動いていない・リーダーが無い機械でも通る (エラーなら返り値の形で出る)。
func TestSystemFrameworkLists(t *testing.T) {
	p := New([]string{DefaultDLL()}, LoadDLL, t.Logf)
	rs, err := p.List()
	if err != nil {
		var e *Error
		if !errors.As(err, &e) {
			t.Fatalf("PCSC.framework を読めません: %v", err)
		}
		t.Logf("PC/SC のエラー (サービスが止まっている機械など): %v", err)
		return
	}
	t.Logf("リーダー %d 個: %v", len(rs), rs)
}
