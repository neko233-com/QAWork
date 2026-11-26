package perf

import (
	"strconv"
	"strings"
)

// ParsePSTime reads ps "time" output, as used by the macOS local sampler:
// [[DD-]hh:]mm:ss[.cc]. It lives in a shared file so the parser is tested on
// every host, even though only darwin calls it in production.
func ParsePSTime(out string) float64 {
	s := strings.TrimSpace(out)
	if s == "" {
		return 0
	}
	days := 0.0
	if i := strings.Index(s, "-"); i > 0 {
		days, _ = strconv.ParseFloat(s[:i], 64)
		s = s[i+1:]
	}
	parts := strings.Split(s, ":")
	var h, m, sec float64
	switch len(parts) {
	case 3:
		h, _ = strconv.ParseFloat(parts[0], 64)
		m, _ = strconv.ParseFloat(parts[1], 64)
		sec, _ = strconv.ParseFloat(parts[2], 64)
	case 2:
		m, _ = strconv.ParseFloat(parts[0], 64)
		sec, _ = strconv.ParseFloat(parts[1], 64)
	case 1:
		sec, _ = strconv.ParseFloat(parts[0], 64)
	}
	return days*86400 + h*3600 + m*60 + sec
}
