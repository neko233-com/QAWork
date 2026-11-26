package perf

import (
	"strconv"
	"strings"
)

// Engine is the game engine QAWork recognized from the target's own
// threads. Thread names are what the runtime publishes, so detection works
// on release builds without hooks, root or a debuggable APK.
type Engine string

const (
	EngineUnknown Engine = ""
	EngineNative  Engine = "native"
	EngineUnity   Engine = "unity"
	EngineUE      Engine = "ue5"
)

// Label is the name the UI and the report show.
func (e Engine) Label() string {
	switch e {
	case EngineUnity:
		return "Unity"
	case EngineUE:
		return "UE5"
	case EngineNative:
		return "原生"
	default:
		return "未识别"
	}
}

// EngineThreads are the threads QAWork reads per frame for each engine:
// the one that runs game logic and the one that submits frames.
type EngineThreads struct {
	Main   string
	Render string
}

// Threads returns the thread names of the engine.
func (e Engine) Threads() EngineThreads {
	switch e {
	case EngineUnity:
		return EngineThreads{Main: "UnityMain", Render: "UnityGfxDeviceW"}
	case EngineUE:
		return EngineThreads{Main: "GameThread", Render: "RenderThread"}
	default:
		return EngineThreads{}
	}
}

// DetectEngine picks the engine out of the thread names a process runs.
// Android truncates comm to 15 characters (on some vendors from the left),
// so names are compared loosely.
func DetectEngine(names []string) Engine {
	var unity, game, render, rhi bool
	for _, n := range names {
		switch {
		case matchName(n, "UnityMain"), matchName(n, "UnityGfxDeviceW"), matchName(n, "UnityGfx"):
			unity = true
		case matchName(n, "GameThread"):
			game = true
		case matchName(n, "RenderThread"):
			render = true
		case matchName(n, "RHIThread"):
			rhi = true
		}
	}
	switch {
	case unity:
		return EngineUnity
	case game && (render || rhi):
		return EngineUE
	case game:
		return EngineUE
	default:
		return EngineNative
	}
}

// matchName reports whether comm, possibly truncated, names want. Exact
// matches first; then only long names may differ by a truncated prefix or
// suffix, so a generic thread called "main" never matches "UnityMain".
func matchName(comm, want string) bool {
	a := strings.ToLower(strings.TrimSpace(comm))
	b := strings.ToLower(strings.TrimSpace(want))
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	const minTruncated = 8
	if len(a) >= minTruncated && (strings.HasPrefix(b, a) || strings.HasSuffix(b, a)) {
		return true
	}
	if len(b) >= minTruncated && (strings.HasPrefix(a, b) || strings.HasSuffix(a, b)) {
		return true
	}
	return false
}

// threadStat is one thread's cumulative CPU ticks, named as the process
// published it.
type threadStat struct {
	TID   int
	Name  string
	Ticks float64
}

// namesOf lists the thread names of the stats.
func namesOf(stats []threadStat) []string {
	names := make([]string, 0, len(stats))
	for _, st := range stats {
		names = append(names, st.Name)
	}
	return names
}

// ParseThreadStats reads the output of
//
//	for t in /proc/<pid>/task/*; do echo @@$(cat $t/comm)@@$(cat $t/stat); done
//
// which is how QAWork sees per-thread CPU without root.
func ParseThreadStats(out string) []threadStat {
	var stats []threadStat
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "@@") {
			continue
		}
		parts := strings.SplitN(line, "@@", 3)
		if len(parts) < 3 || strings.TrimSpace(parts[2]) == "" {
			continue
		}
		name := strings.TrimSpace(parts[1])
		st, err := ParseProcStat(parts[2])
		if err != nil {
			continue
		}
		tid := 0
		if i := strings.Index(parts[2], " ("); i > 0 {
			head := strings.Fields(parts[2][:i])
			if len(head) > 0 {
				tid, _ = strconv.Atoi(head[0])
			}
		}
		stats = append(stats, threadStat{TID: tid, Name: name, Ticks: st.CPUTicks})
	}
	return stats
}

// keyThread returns the thread stat whose name matches want, and whether
// it was found. Longest name match wins, so "UnityGfxDeviceW" is not
// confused with a shorter prefix.
func keyThread(stats []threadStat, want string) (threadStat, bool) {
	if want == "" {
		return threadStat{}, false
	}
	var best threadStat
	var ok bool
	for _, st := range stats {
		if matchName(st.Name, want) {
			if !ok || len(st.Name) > len(best.Name) {
				best, ok = st, true
			}
		}
	}
	return best, ok
}
