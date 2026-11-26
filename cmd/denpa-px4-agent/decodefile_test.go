package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func tsPackets(n int, tsc byte) []byte {
	p := make([]byte, 188)
	p[0], p[3] = 0x47, tsc<<6|0x10
	return bytes.Repeat(p, n)
}

// 設定ファイルや tls/ を実際の置き場に作らないよう、家をテスト用にする
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DENPA_HOME", dir)
	t.Setenv("TUNERS_FILE", filepath.Join(dir, "config", "tuners.json"))
	t.Setenv("CARD_URL", "")
	return dir
}

func TestDecodeFile_引数(t *testing.T) {
	isolate(t)
	if code := decodeFile(nil); code != 2 {
		t.Errorf("引数なし = %d, want 2", code)
	}
	if code := decodeFile([]string{"a", "b", "c"}); code != 2 {
		t.Errorf("多すぎ = %d, want 2", code)
	}
	if code := decodeFile([]string{filepath.Join(t.TempDir(), "nothing.ts")}); code != 1 {
		t.Errorf("入力が無い = %d, want 1", code)
	}
}

func TestDecodeFile_数えるだけ(t *testing.T) {
	dir := isolate(t)
	in := filepath.Join(dir, "in.ts")
	os.WriteFile(in, append(tsPackets(3, 2), tsPackets(2, 0)...), 0o644)
	if code := decodeFile([]string{in}); code != 0 {
		t.Errorf("数えるだけは 0 のはず: %d", code)
	}
	// 数えるだけなら、何も書かない
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("余計なファイルができた: %v", entries)
	}
}

func TestDecodeFile_復号する(t *testing.T) {
	dir := isolate(t)
	clear, scrambled := filepath.Join(dir, "clear.ts"), filepath.Join(dir, "scrambled.ts")
	os.WriteFile(clear, tsPackets(10, 0), 0o644)
	os.WriteFile(scrambled, tsPackets(10, 2), 0o644)

	// スクランブルされていなければ通る
	out := filepath.Join(dir, "clear.out.ts")
	if code := decodeFile([]string{clear, out}); code != 0 {
		t.Fatalf("clear = %d", code)
	}
	if b, _ := os.ReadFile(out); !bytes.Equal(b, tsPackets(10, 0)) {
		t.Errorf("そのまま書くはず (%d バイト)", len(b))
	}

	// もうあるファイルは上書きしない
	if code := decodeFile([]string{clear, out}); code != 1 {
		t.Errorf("上書き = %d, want 1", code)
	}
	if b, _ := os.ReadFile(out); !bytes.Equal(b, tsPackets(10, 0)) {
		t.Error("上書きされた")
	}

	// 同じファイル
	if code := decodeFile([]string{clear, clear}); code != 2 {
		t.Errorf("同じファイル = %d, want 2", code)
	}
	if b, _ := os.ReadFile(clear); !bytes.Equal(b, tsPackets(10, 0)) {
		t.Error("入力が壊れた")
	}

	// 復号できなければ 1 で、書きかけの出力は消す
	bad := filepath.Join(dir, "scrambled.out.ts")
	if code := decodeFile([]string{scrambled, bad}); code != 1 {
		t.Errorf("復号できない = %d, want 1", code)
	}
	if _, err := os.Stat(bad); err == nil {
		t.Error("復号できなかった出力が残っている")
	}
}
