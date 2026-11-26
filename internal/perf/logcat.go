package perf

import "strings"

// AnalyzeLogcat flags a crash or ANR only when the package is tied to the
// event. An ANR line must name the package; a FATAL EXCEPTION must contain the
// package or be followed by a "Process: pkg" line. Unrelated crashes do not
// poison the result.
func AnalyzeLogcat(out, pkg string) (crash, anr bool) {
	if pkg == "" {
		return false, false
	}
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if strings.Contains(line, "ANR in ") && strings.Contains(line, pkg) {
			anr = true
		}
		if !strings.Contains(line, "FATAL EXCEPTION") {
			continue
		}
		if strings.Contains(line, pkg) {
			crash = true
			continue
		}
		for j := i + 1; j < len(lines) && j <= i+5; j++ {
			if strings.Contains(lines[j], "Process: ") && strings.Contains(lines[j], pkg) {
				crash = true
				break
			}
		}
	}
	return crash, anr
}
