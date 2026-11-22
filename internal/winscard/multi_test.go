package winscard

import (
	"reflect"
	"testing"
)

func TestSplitMultiUTF8(t *testing.T) {
	b := []byte("ACS ACR39U 00 00\x00PLEX リーダー\x00\x00")
	if got, want := splitMultiUTF8(b), []string{"ACS ACR39U 00 00", "PLEX リーダー"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := splitMultiUTF8([]byte{0, 0}); len(got) != 0 {
		t.Errorf("空の並び = %q", got)
	}
	// NUL で終わらない名前は、途中で切れたものとして拾わない
	if got := splitMultiUTF8([]byte("A\x00B")); !reflect.DeepEqual(got, []string{"A"}) {
		t.Errorf("途中で切れた名前を拾いました: %q", got)
	}
}
