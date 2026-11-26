package perf

import "testing"

func TestParsePSTime(t *testing.T) {
	cases := map[string]float64{
		"":           0,
		"00:00.50":   0.5,
		"0:01.23":    1.23,
		"01:02":      62,
		"01:02:03":   3723,
		"2-01:02:03": 2*86400 + 3723,
		"garbage":    0,
	}
	for in, want := range cases {
		if got := ParsePSTime(in); got != want {
			t.Errorf("ParsePSTime(%q) = %v, want %v", in, got, want)
		}
	}
}
