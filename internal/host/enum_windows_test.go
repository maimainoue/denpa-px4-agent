//go:build windows

package host

import (
	"os"
	"testing"
)

// 実際の SetupAPI で、USB デバイスを数えられること (機材が無くても、1 台以上の USB デバイスはあるはず)。
func TestSetupEnum_USBデバイスを列挙できる(t *testing.T) {
	paths, err := NewEnumerator().USBDevices()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Skip("USB デバイスが無い環境")
	}
	n := 0
	for _, p := range paths {
		if usbID(p) != "" {
			n++
		}
	}
	if n == 0 {
		t.Errorf("vid_・pid_ を含むパスが 1 つも無い: %v", paths)
	}
	t.Logf("USB デバイス %d 台 (vid/pid 付き %d)", len(paths), n)
	if os.Getenv("DENPA_HOST_DETECT_LOG") != "" {
		for _, p := range paths {
			t.Log(p)
		}
	}
}

func TestSetupEnum_存在しないGUIDは0台(t *testing.T) {
	paths, err := NewEnumerator().InterfacePaths("{00000000-1234-1234-1234-000000000001}")
	if err != nil || len(paths) != 0 {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	if _, err := NewEnumerator().InterfacePaths("not-a-guid"); err == nil {
		t.Error("GUID でない文字列はエラーのはず")
	}
}
