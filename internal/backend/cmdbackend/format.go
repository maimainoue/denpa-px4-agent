package cmdbackend

import (
	"fmt"
	"strconv"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/channel"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/devices"
)

// FormatError はチャンネル名をそのコマンドの書き方に直せないとき。denpa には 400 で返す。
type FormatError struct {
	// TSID は `:TSID` を渡せないことが理由か。
	TSID    bool
	Channel string
}

func (e *FormatError) Error() string {
	if e.TSID {
		return "このコマンドでは TSID を指定できません"
	}
	return fmt.Sprintf("このコマンドでは %s を選局できません", e.Channel)
}

// FormatChannel はチャンネル名を channelFormat の引数に直す (仕様書 6.2)。
//
// allowTSID は機材の定義に tsidArg があるとき true。TSID は別の引数で渡すので、
// チャンネルの引数には入れない (`BS15:16625` → `BS15_0` と `--tsid 16625`)。
func FormatChannel(format string, ch channel.Channel, allowTSID bool) ([]string, error) {
	no := func() ([]string, error) { return nil, &FormatError{Channel: ch.String()} }

	if ch.TSID >= 0 && format != devices.FormatDenpa && !allowTSID {
		return nil, &FormatError{TSID: true, Channel: ch.String()}
	}

	switch format {
	case devices.FormatDenpa:
		return []string{ch.String()}, nil

	case devices.FormatRecpt1:
		switch ch.Prefix {
		case "T":
			return []string{strconv.Itoa(ch.Number)}, nil
		case "C":
			// 地上波のパススルー。ISDB-C は recpt1 では選局できない
			return []string{ch.Name()}, nil
		case "BS":
			return []string{fmt.Sprintf("BS%d_0", ch.Number)}, nil
		case "CS":
			return []string{fmt.Sprintf("CS%d", ch.Number)}, nil
		}
		return no()

	case devices.FormatRecisdb:
		switch ch.Prefix {
		case "T", "C":
			return []string{ch.Name()}, nil
		case "BS":
			return []string{fmt.Sprintf("BS%02d_0", ch.Number)}, nil
		case "CS":
			return []string{fmt.Sprintf("CS%02d", ch.Number)}, nil
		case "QV", "QC", "QU":
			k := max(ch.Stream, 0)
			return []string{fmt.Sprintf("ISDBC-%s%d_%d", ch.Prefix[1:], ch.Number, k)}, nil
		case "H":
			return []string{fmt.Sprintf("J83B-H%03d", ch.Number)}, nil
		}
		return no()

	case devices.FormatPx4rec:
		switch ch.Prefix {
		case "T":
			return []string{"T", strconv.Itoa(ch.Number)}, nil
		case "BS":
			return []string{"S", strconv.Itoa((ch.Number - 1) / 2)}, nil
		case "CS":
			return []string{"S", strconv.Itoa(12 + (ch.Number-2)/2)}, nil
		case "QC":
			args := []string{"C", strconv.Itoa(ch.Number)}
			if ch.Stream > 0 {
				args = append(args, "--tsmf", strconv.Itoa(ch.Stream))
			}
			return args, nil
		case "H":
			return []string{"J", strconv.Itoa(ch.Number)}, nil
		}
		// C (パススルー)・QV・QU は px4rec に渡せない
		return no()
	}
	return nil, fmt.Errorf("channelFormat を読めません (%s)", format)
}
