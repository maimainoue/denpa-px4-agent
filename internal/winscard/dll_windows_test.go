//go:build windows

package winscard

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSplitMulti(t *testing.T) {
	enc := func(s string) []uint16 {
		var u []uint16
		for _, r := range s {
			u = append(u, uint16(r))
		}
		return u
	}
	// "A\0B C\0\0"
	b := append(enc("A"), 0)
	b = append(b, enc("B C")...)
	b = append(b, 0, 0)
	if got, want := splitMulti(b), []string{"A", "B C"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := splitMulti([]uint16{0, 0}); len(got) != 0 {
		t.Errorf("空の並び = %q", got)
	}
}

func TestLoadDLLRejectsRelativeAndMissing(t *testing.T) {
	if _, err := LoadDLL(`winscard.dll`); err == nil || !strings.Contains(err.Error(), "絶対パス") {
		t.Errorf("相対パスを受けました: %v", err)
	}
	if _, err := LoadDLL(filepath.Join(t.TempDir(), "nothing.dll")); err == nil {
		t.Error("無い DLL を読めました")
	}
}

// 設定に `/` 区切りで書かれたパスでも読める (LoadLibraryEx は `\` を求める)。
func TestLoadDLLAcceptsForwardSlashes(t *testing.T) {
	p := strings.ReplaceAll(DefaultDLL(), `\`, "/")
	if p == DefaultDLL() {
		t.Fatalf("`/` に直せていません: %s", p)
	}
	if _, err := LoadDLL(p); err != nil {
		t.Errorf("%s を読めません: %v", p, err)
	}
}

// 本物の System32\winscard.dll を読んで、リーダーの一覧を取る。
// Smart Card サービスが動いていない・リーダーが無い機械でも通る (エラーなら返り値の形で出る)。
func TestSystemWinscardLists(t *testing.T) {
	p := New([]string{DefaultDLL()}, LoadDLL, t.Logf)
	rs, err := p.List()
	if err != nil {
		var e *Error
		if !errors.As(err, &e) {
			t.Fatalf("DLL を読めません: %v", err)
		}
		t.Logf("PC/SC のエラー (サービスが止まっている機械など): %v", err)
		return
	}
	t.Logf("リーダー %d 個: %v", len(rs), rs)
}
