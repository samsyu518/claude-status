package format

import (
	"testing"
	"time"
)

func TestResetsIn(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		t    time.Time
		want string
	}{
		{"past", now.Add(-time.Second), "resetting…"},
		{"now", now, "resetting…"},
		{"seconds only", now.Add(45*time.Second + 100*time.Millisecond), "45s"},
		{"minutes and seconds", now.Add(12*time.Minute + 34*time.Second + 100*time.Millisecond), "12m 34s"},
		{"minutes only (round)", now.Add(12*time.Minute + 600*time.Millisecond), "12m 01s"}, // Ensure it's comfortably > 0.5s
		{"hour boundary (low)", now.Add(59*time.Minute + 59*time.Second + 100*time.Millisecond), "59m 59s"},
		{"hour boundary (high)", now.Add(time.Hour + 40*time.Second), "1h 01m"}, // Comfortably rounds up to 1m
		{"hours and minutes", now.Add(3*time.Hour + 25*time.Minute + 10*time.Second), "3h 25m"},
		{"days and hours", now.Add(2*24*time.Hour + 5*time.Hour + 10*time.Minute), "2d 5h"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResetsIn(tc.t)
			if got != tc.want {
				t.Errorf("ResetsIn(%v) = %q, want %q", tc.t, got, tc.want)
			}
		})
	}
}
