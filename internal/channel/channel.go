// Package channel は物理チャンネル名 (T27・BS15:16625・QC30:2・H44 など) を読む。
// 書き方は仕様書 7.1 のとおり。
package channel

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Type は受信の種別。
type Type string

const (
	GR   Type = "GR"
	BS   Type = "BS"
	CS   Type = "CS"
	CATV Type = "CATV"
	SKY  Type = "SKY"
)

// Valid は種別として正しい文字列か。
func (t Type) Valid() bool {
	switch t {
	case GR, BS, CS, CATV, SKY:
		return true
	}
	return false
}

// Channel は読んだチャンネル名。
type Channel struct {
	Type   Type
	Prefix string // T C BS CS QV QC QU H
	Number int
	// TSID は衛星の `:TSID`。無ければ -1。
	TSID int
	// Stream は ISDB-C の `:k` (TSMF の k 本目)。無ければ -1。
	Stream int
}

// Name は `:TSID` や `:k` を除いた名前 (T27・BS15・CS02・QC30・H44)。
func (c Channel) Name() string {
	switch c.Prefix {
	case "BS", "CS", "H":
		return fmt.Sprintf("%s%02d", c.Prefix, c.Number)
	}
	return fmt.Sprintf("%s%d", c.Prefix, c.Number)
}

// String は `:TSID` や `:k` を含む正規の名前。
func (c Channel) String() string {
	switch {
	case c.TSID >= 0:
		return fmt.Sprintf("%s:%d", c.Name(), c.TSID)
	case c.Stream >= 0:
		return fmt.Sprintf("%s:%d", c.Name(), c.Stream)
	}
	return c.Name()
}

// Error は読めなかったときの理由。denpa には 400 で返す。
type Error struct {
	Msg string
}

func (e *Error) Error() string { return e.Msg }

var pattern = regexp.MustCompile(`^(T|C|BS|CS|QV|QC|QU|H)(\d{1,3})(?::(\d{1,5}))?$`)

func unreadable(input string) *Error {
	return &Error{Msg: input + ": チャンネル名を読めません"}
}

// Parse はチャンネル名を読む。大文字小文字は区別しない。
func Parse(input string) (Channel, error) {
	name := strings.ToUpper(strings.TrimSpace(input))
	m := pattern.FindStringSubmatch(name)
	if m == nil {
		return Channel{}, unreadable(input)
	}
	num, _ := strconv.Atoi(m[2])
	c := Channel{Prefix: m[1], Number: num, TSID: -1, Stream: -1}
	suffix := -1
	if m[3] != "" {
		suffix, _ = strconv.Atoi(m[3])
	}

	inRange := func(lo, hi int) bool { return num >= lo && num <= hi }
	switch c.Prefix {
	case "T":
		c.Type = GR
		if !inRange(13, 62) || suffix >= 0 {
			return Channel{}, unreadable(input)
		}
	case "C":
		c.Type = GR
		if !inRange(13, 63) || suffix >= 0 {
			return Channel{}, unreadable(input)
		}
	case "BS":
		c.Type = BS
		if num == 7 || num == 17 {
			return Channel{}, &Error{Msg: input + ": BS07 と BS17 は 4K/8K のため選局できません"}
		}
		if !inRange(1, 23) || num%2 == 0 || suffix > 65535 {
			return Channel{}, unreadable(input)
		}
		c.TSID = suffix
	case "CS":
		c.Type = CS
		if !inRange(2, 24) || num%2 != 0 || suffix > 65535 {
			return Channel{}, unreadable(input)
		}
		c.TSID = suffix
	case "QV", "QC", "QU":
		c.Type = CATV
		ok := false
		switch c.Prefix {
		case "QV":
			ok = inRange(1, 12)
		case "QC":
			ok = inRange(13, 63)
		case "QU":
			ok = inRange(13, 62)
		}
		if !ok || suffix > 14 {
			return Channel{}, unreadable(input)
		}
		c.Stream = suffix
	case "H":
		c.Type = SKY
		if !inRange(1, 105) || suffix >= 0 {
			return Channel{}, unreadable(input)
		}
	}
	return c, nil
}
