// Package store は tuners.json・channels.json・cards.json の読み書き (仕様書 12.2)。
// 書くときは .writing に書いてから rename する。
package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
)

// WriteJSON は v を JSON で書く。日本語はエスケープしない。
func WriteJSON(path string, v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".writing"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadJSON は読めたら true。ファイルが無ければ false。読めない・形式が違うときは理由を返す。
func ReadJSON(path string, v any) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, err
	}
	return true, nil
}

// Tuners は tuners.json。
type Tuners struct{ Path string }

// Load は定義を読む。1 台ぶんの書き損じはその台だけ飛ばす。定義が無ければ found=false。
func (t Tuners) Load(logf backend.Logf) (specs []backend.TunerSpec, found bool) {
	var f struct {
		Tuners []json.RawMessage `json:"tuners"`
	}
	ok, err := ReadJSON(t.Path, &f)
	if err != nil {
		logf("tuners.json を読めません。無いものとして続けます: %v", err)
		return nil, false
	}
	if !ok {
		return nil, false
	}
	for i, raw := range f.Tuners {
		var s backend.TunerSpec
		if err := json.Unmarshal(raw, &s); err != nil || s.Name == "" {
			logf("tuners.json の %d 番目を読めません。この台は飛ばします", i+1)
			continue
		}
		specs = append(specs, s)
	}
	return specs, len(specs) > 0
}

// Save は定義を書く。
func (t Tuners) Save(specs []backend.TunerSpec) error {
	if specs == nil {
		specs = []backend.TunerSpec{}
	}
	return WriteJSON(t.Path, map[string]any{"tuners": specs})
}

// Remove は定義を消す (自動検出に戻す)。
func (t Tuners) Remove() error {
	if err := os.Remove(t.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Channel は預かるチャンネル 1 件。中身は解釈しない。
type Channel map[string]any

var typeOrder = map[string]int{"GR": 0, "BS": 1, "CS": 2, "CATV": 3, "SKY": 4}

// Channels は channels.json。
type Channels struct {
	Path string
	mu   sync.Mutex
}

func (c *Channels) read() []Channel {
	data, err := os.ReadFile(c.Path)
	if err != nil {
		return []Channel{}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var out []Channel
	if dec.Decode(&out) != nil || out == nil {
		return []Channel{}
	}
	return out
}

// List は預かっているチャンネル。
func (c *Channels) List() []Channel {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.read()
}

func str(v any) string { s, _ := v.(string); return s }

func sortChannels(list []Channel) {
	sort.SliceStable(list, func(i, j int) bool {
		ti, tj := typeOrder[str(list[i]["type"])], typeOrder[str(list[j]["type"])]
		if ti != tj {
			return ti < tj
		}
		return str(list[i]["channel"]) < str(list[j]["channel"])
	})
}

// Replace は scanned にある種別の項目だけ入れ替える (10.5)。
func (c *Channels) Replace(items []Channel, scanned []string) ([]Channel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sc := map[string]bool{}
	for _, s := range scanned {
		sc[s] = true
	}
	out := []Channel{}
	for _, ch := range c.read() {
		if !sc[str(ch["type"])] {
			out = append(out, ch)
		}
	}
	for _, ch := range items {
		if sc[str(ch["type"])] {
			out = append(out, ch)
		}
	}
	sortChannels(out)
	if err := WriteJSON(c.Path, out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteType は 1 つの種別を全部消す。
func (c *Channels) DeleteType(typ string) ([]Channel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []Channel{}
	for _, ch := range c.read() {
		if str(ch["type"]) != typ {
			out = append(out, ch)
		}
	}
	if err := WriteJSON(c.Path, out); err != nil {
		return nil, err
	}
	return out, nil
}

// Cards は cards.json。
type Cards struct {
	Path string
	mu   sync.Mutex
}

// Disabled は使わないリーダー名。
func (c *Cards) Disabled() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var f struct {
		Disabled []string `json:"disabled"`
	}
	if _, err := ReadJSON(c.Path, &f); err != nil {
		return nil
	}
	return f.Disabled
}

// SetDisabled は使わないリーダーを書く。並びは文字列の順。
func (c *Cards) SetDisabled(names []string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	seen := map[string]bool{}
	out := []string{}
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	if err := WriteJSON(c.Path, map[string]any{"disabled": out}); err != nil {
		return nil, fmt.Errorf("cards.json を書けません: %w", err)
	}
	return out, nil
}
