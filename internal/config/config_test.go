package config

import "testing"

func TestLoad_STREAM_QUEUE_BYTES(t *testing.T) {
	load := func(v string) (*Config, error) {
		return Load(func(k string) string {
			if k == "STREAM_QUEUE_BYTES" {
				return v
			}
			return ""
		}, "linux", "/usr/local/sbin/denpa-px4-agent")
	}
	c, err := load("")
	if err != nil || c.StreamQueueBytes != 0 {
		t.Fatalf("既定は 0 (manager が 64MB にする): %+v %v", c, err)
	}
	if c, err = load("16777216"); err != nil || c.StreamQueueBytes != 16<<20 {
		t.Errorf("%+v %v", c, err)
	}
	for _, bad := range []string{"abc", "1000", "-5"} {
		if _, err := load(bad); err == nil {
			t.Errorf("%q は断る", bad)
		}
	}
}
