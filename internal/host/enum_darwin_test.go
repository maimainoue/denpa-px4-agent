//go:build darwin

package host

import (
	"errors"
	"testing"
)

// 実際の IOKit で、USB デバイスを数えられること。機材が 1 台も無い環境 (CI) では、数えられることだけを確かめる。
func TestIOKitEnum_USBデバイスを列挙できる(t *testing.T) {
	paths, err := NewEnumerator().USBDevices()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if usbID(p) == "" {
			t.Errorf("vid_・pid_ を読めない: %q", p)
		}
	}
	t.Logf("USB デバイス %d 台: %v", len(paths), paths)
}

func TestIOKitEnum_InterfacePathsは使えない旨を返す(t *testing.T) {
	if _, err := NewEnumerator().InterfacePaths("{00000000-0000-0000-0000-000000000000}"); !errors.Is(err, ErrNoInterface) {
		t.Fatalf("err = %v", err)
	}
}
