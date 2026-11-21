package devices

import (
	"strings"
	"testing"
)

func httpDef(edit func(*Def)) Def {
	d := Def{
		Model:         "h",
		URL:           "http://{device}:8301/tuner/{channel}{sidQuery}",
		ChannelFormat: FormatRecpt1,
		Receivers:     []Receiver{{Index: 0, Types: []string{"GR"}}},
	}
	if edit != nil {
		edit(&d)
	}
	return d
}

func TestValidate_urlは通る(t *testing.T) {
	d := httpDef(nil)
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if !d.IsCmd() || !d.IsHTTP() {
		t.Error("url の機種は cmd バックエンドの機種で、HTTP")
	}
}

func TestValidate_urlの書き損じ(t *testing.T) {
	code := 22
	for name, tc := range map[string]struct {
		edit func(*Def)
		want string
	}{
		"commandと同時":  {func(d *Def) { d.Command = []string{"curl"} }, "同時に書けません"},
		"httpsは受けない":   {func(d *Def) { d.URL = "https://{device}/x" }, "http:// で始めます"},
		"スキームだけ":      {func(d *Def) { d.URL = "http://" }, "http:// で始めます"},
		"serviceArg":   {func(d *Def) { d.ServiceArg = []string{"--sid", "{service}"} }, "使いません"},
		"tsidArg":      {func(d *Def) { d.TsidArg = []string{"--tsid", "{tsid}"} }, "使いません"},
		"busyExitCode": {func(d *Def) { d.BusyExitCode = &code }, "使いません"},
	} {
		d := httpDef(tc.edit)
		err := d.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v (want %q を含む)", name, err, tc.want)
		}
	}
}
