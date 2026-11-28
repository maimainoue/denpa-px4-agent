package devices

import (
	"fmt"
	"strings"
)

// 自動検出の対象 (仕様書 4.2・5.3)。
//
// denpa 付属のエージェントが自分で開く機種 (Def.Denpa) は、自動検出の既定の対象から外す。
// 同じ機材を denpa 付属のエージェントとこのエージェントの両方が一覧に並べて、取り合うのを避けるため。
// 対象にするには、環境変数 DETECT_INCLUDE に機種名 (か `denpa`) を書く。
// tuners.json に書いた機材は、書いた時点で明示しているので、いつも対象になる。

// IncludeAllDenpa は DETECT_INCLUDE に書く、denpa が対応している機種を全部対象にする印。
const IncludeAllDenpa = "denpa"

// denpaUSB は、denpa 付属のエージェントが USB を直に開く機材の USB の ID (小文字)。
// denpa の agent/Denpa.Agent の It930x.cs (IT930x の機材。PLEX・e-Better・Digibest)・Siano.cs (3275:0080)・
// WinUsbTargets.cs (Sony CECH-ZD1 J) の一覧から写した。denpa の対応が増えたら、ここも足す。
// PIX-DT295 は Dt295.cs の Rev.01 (103b)・Rev.02 (105c)。103c (W)・1038 (DT420)・1039 (DT410) は denpa も未確認として断るので入れない。
var denpaUSB = map[string]bool{
	"0511:084e": true, "0511:024e": true, "0511:924e": true, // PX-MLT5U・PX-MLT5PE・DTV02A-5TS-P
	"0511:083f": true, "0511:023f": true, "0511:073f": true, // PX-W3U4・PX-W3PE4・PX-W3PE5
	"0511:084a": true, "0511:024a": true, "0511:074a": true, // PX-Q3U4・PX-Q3PE4・PX-Q3PE5
	"0511:0253": true, "0511:0252": true, "0511:0254": true, // PX-MLT8PE・DTV02A-4TS-P
	"0511:004b": true, "0511:084b": true, // ISDB2056・ISDB2056N (DTV02-1T1S-U・DTV02A-1T1S-U)
	"0511:0854": true, "0511:0855": true, // PX-M1UR・PX-S1UR
	"0511:0052": true, // DTV03A-1TU
	"3275:0080": true, // PX-S1UD (Siano RIO)
	"054c:04b2": true, // Sony CECH-ZD1 J
	"06b8:103b": true, "06b8:105c": true, // PIX-DT295 Rev.01・Rev.02
}

// markDenpa は、denpa が対応している機種に印を付ける (USB の ID で決める)。
func markDenpa(s *Defs) {
	for i := range s.list {
		for _, u := range s.list[i].USB {
			if denpaUSB[strings.ToLower(u)] {
				s.list[i].Denpa = true
			}
		}
	}
}

// SetDetectInclude は DETECT_INCLUDE の中身 (機種名か `denpa`) を覚える。機種名は大文字・小文字を区別しない。
// 機種表に無い名前は、書き損じなので unknown で返す。
func (s *Defs) SetDetectInclude(names []string) (unknown []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.include = map[string]bool{}
	s.includeDenpa = false
	for _, n := range names {
		n = strings.TrimSpace(n)
		switch {
		case n == "":
		case strings.EqualFold(n, IncludeAllDenpa):
			s.includeDenpa = true
		default:
			found := false
			for _, d := range s.list {
				if strings.EqualFold(d.Model, n) {
					found = true
				}
			}
			if !found {
				unknown = append(unknown, n)
			}
			s.include[strings.ToLower(n)] = true
		}
	}
	return unknown
}

// Detectable は、自動検出の対象にするか。denpa が対応していない機種は、いつも対象。
func (s *Defs) Detectable(d *Def) bool {
	if !d.Denpa {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.includeDenpa || s.include[strings.ToLower(d.Model)]
}

// SkipDetect は、対象外にした機材を覚える (`--devices` と記録に出す)。id は同じ機材を数え直さないための印。
func (s *Defs) SkipDetect(model, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.skipped == nil {
		s.skipped = map[string]map[string]struct{}{}
	}
	if s.skipped[model] == nil {
		s.skipped[model] = map[string]struct{}{}
	}
	s.skipped[model][id] = struct{}{}
}

// ResetSkipped は、対象外にした機材の覚えを消す (検出のたびに最初に呼ぶ)。
func (s *Defs) ResetSkipped() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skipped = nil
}

// SkippedModel は、自動検出の対象外にした機種と台数。
type SkippedModel struct {
	Model string
	N     int
}

// Skipped は、対象外にした機材を機種ごとに、機種表の順で返す。
func (s *Defs) Skipped() []SkippedModel {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []SkippedModel
	for _, d := range s.list {
		if n := len(s.skipped[d.Model]); n > 0 {
			out = append(out, SkippedModel{d.Model, n})
		}
	}
	return out
}

// SkippedText は `PLEX PX-W3U4 ×2, Sony CECH-ZD1 J ×1` の形の文字列 (無ければ空)。
func SkippedText(list []SkippedModel) string {
	parts := make([]string, 0, len(list))
	for _, m := range list {
		parts = append(parts, fmt.Sprintf("%s ×%d", m.Model, m.N))
	}
	return strings.Join(parts, ", ")
}
