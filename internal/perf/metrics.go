// Package perf samples a target app the way PerfDog does: frame rate from
// the platform's own frame timestamps, CPU and memory from the target
// process, and temperature from the device.
//
// Every sampler returns one Sample per interval. The QAWork GUI and the
// qawork CLI share the same samplers, so a capture taken from the window
// and a capture taken in CI produce the same CSV.
package perf

import (
	"math"
	"sort"
	"time"
)

// Sample is one interval's worth of measurements.
type Sample struct {
	At      time.Time `json:"at"`
	FPS     float64   `json:"fps"`
	FrameMS float64   `json:"frame_ms"`
	JankPct float64   `json:"jank_pct"`
	CPU     float64   `json:"cpu_pct"`
	MemMB   float64   `json:"mem_mb"`
	TempC   float64   `json:"temp_c"`
	Threads int       `json:"threads"`

	// Engine is what the target's threads say it runs: Unity, UE5, or
	// native. Main and Render are that engine's key threads, with their
	// CPU share for this interval, so a Unity or UE5 run answers "主线程
	// 卡不卡" and not only "进程多忙".
	Engine       Engine  `json:"engine,omitempty"`
	MainThread   string  `json:"main_thread,omitempty"`
	MainCPU      float64 `json:"main_cpu"`
	RenderThread string  `json:"render_thread,omitempty"`
	RenderCPU    float64 `json:"render_cpu"`

	Note string `json:"note,omitempty"`
}

// Summary aggregates a capture. Percentiles are of frame time, the way
// PerfDog reports 帧耗时: P95 is the frame time 95% of frames stay under.
type Summary struct {
	Samples         int     `json:"samples"`
	DurationSeconds float64 `json:"duration_seconds"`
	FPSMean         float64 `json:"fps_mean"`
	FPSMin          float64 `json:"fps_min"`
	FrameP50        float64 `json:"frame_p50"`
	FrameP95        float64 `json:"frame_p95"`
	FrameP99        float64 `json:"frame_p99"`
	FrameSamples    int     `json:"frame_samples"`
	JankPct         float64 `json:"jank_pct"`
	CPUMax          float64 `json:"cpu_max"`
	CPUMean         float64 `json:"cpu_mean"`
	MemMaxMB        float64 `json:"mem_max_mb"`
	TempMax         float64 `json:"temp_max"`

	Engine       Engine  `json:"engine,omitempty"`
	EngineLabel  string  `json:"engine_label,omitempty"`
	MainThread   string  `json:"main_thread,omitempty"`
	RenderThread string  `json:"render_thread,omitempty"`
	MainCPUMax   float64 `json:"main_cpu_max"`
	RenderCPUMax float64 `json:"render_cpu_max"`
}

// Summarize aggregates samples. A zero FPS sample means the app rendered
// nothing in that interval (idle screen, sampler gap) and is left out of
// the frame statistics, but still counted in the sample count.
func Summarize(samples []Sample) Summary {
	s := Summary{Samples: len(samples)}
	if len(samples) == 0 {
		return s
	}
	s.DurationSeconds = samples[len(samples)-1].At.Sub(samples[0].At).Seconds()
	var cpuSum float64
	var fpsCount int
	var frames []float64
	for _, v := range samples {
		if v.Engine != "" {
			s.Engine = v.Engine
			s.EngineLabel = v.Engine.Label()
		}
		if v.MainThread != "" {
			s.MainThread = v.MainThread
		}
		if v.RenderThread != "" {
			s.RenderThread = v.RenderThread
		}
		if v.MainCPU > s.MainCPUMax {
			s.MainCPUMax = v.MainCPU
		}
		if v.RenderCPU > s.RenderCPUMax {
			s.RenderCPUMax = v.RenderCPU
		}
		if v.FPS > 0 {
			fpsCount++
			s.FPSMean += v.FPS
			frames = append(frames, v.FrameMS)
			if s.FPSMin == 0 || v.FPS < s.FPSMin {
				s.FPSMin = v.FPS
			}
		}
		if v.CPU > s.CPUMax {
			s.CPUMax = v.CPU
		}
		cpuSum += v.CPU
		if v.MemMB > s.MemMaxMB {
			s.MemMaxMB = v.MemMB
		}
		if v.TempC > s.TempMax {
			s.TempMax = v.TempC
		}
		s.JankPct += v.JankPct
	}
	if fpsCount > 0 {
		s.FPSMean /= float64(fpsCount)
	}
	s.FrameSamples = fpsCount
	s.CPUMean = cpuSum / float64(len(samples))
	s.JankPct /= float64(len(samples))
	sort.Float64s(frames)
	s.FrameP50 = percentile(frames, 0.50)
	s.FrameP95 = percentile(frames, 0.95)
	s.FrameP99 = percentile(frames, 0.99)
	return s
}

// percentile returns the p-th percentile of a sorted slice, and 0 when the
// slice is empty.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	i := int(math.Round(p * float64(len(sorted)-1)))
	return sorted[i]
}

// DropRate is how far the mean frame rate stays below nominal, as a
// percentage: 0 when the app holds the target, 50 at half of it.
func DropRate(fpsMean, nominal float64) float64 {
	if nominal <= 0 {
		return 0
	}
	return math.Max(0, 100*(1-fpsMean/nominal))
}

// Thresholds are the acceptance limits of a run, the numbers a gate in CI
// compares against.
type Thresholds struct {
	MinFPSMean float64
	MaxJankPct float64
	MaxCPU     float64
	MaxMemMB   float64
	MaxTempC   float64
}

// DefaultThresholds are the limits QAWork starts with: 48 fps mean, 5%
// janky frames, 70% CPU, 1 GiB of memory, 45 °C.
func DefaultThresholds() Thresholds {
	return Thresholds{MinFPSMean: 48, MaxJankPct: 5, MaxCPU: 70, MaxMemMB: 1024, MaxTempC: 45}
}

// Check is one threshold compared against a run.
type Check struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
	Limit float64 `json:"limit"`
	Unit  string  `json:"unit"`
	OK    bool    `json:"ok"`
}

// Evaluate compares a summary against the thresholds. Checks whose limit
// is zero are skipped, as is the frame rate check when the sampler had no
// frame source at all (a PC process without a Present hook), and the
// temperature check when the device reported no temperature.
func (t Thresholds) Evaluate(s Summary) []Check {
	var out []Check
	if t.MinFPSMean > 0 && s.FrameSamples > 0 {
		out = append(out, Check{Name: "平均帧率", Value: s.FPSMean, Limit: t.MinFPSMean, Unit: "fps", OK: s.FPSMean >= t.MinFPSMean})
	}
	if t.MaxJankPct > 0 && s.FrameSamples > 0 {
		out = append(out, Check{Name: "卡顿帧占比", Value: s.JankPct, Limit: t.MaxJankPct, Unit: "%", OK: s.JankPct <= t.MaxJankPct})
	}
	if t.MaxCPU > 0 {
		out = append(out, Check{Name: "CPU 峰值", Value: s.CPUMax, Limit: t.MaxCPU, Unit: "%", OK: s.CPUMax <= t.MaxCPU})
	}
	if t.MaxMemMB > 0 {
		out = append(out, Check{Name: "内存峰值", Value: s.MemMaxMB, Limit: t.MaxMemMB, Unit: "MB", OK: s.MemMaxMB <= t.MaxMemMB})
	}
	if t.MaxTempC > 0 && s.TempMax > 0 {
		out = append(out, Check{Name: "温度峰值", Value: s.TempMax, Limit: t.MaxTempC, Unit: "°C", OK: s.TempMax <= t.MaxTempC})
	}
	return out
}

// Failed reports whether any check failed.
func Failed(checks []Check) bool {
	for _, c := range checks {
		if !c.OK {
			return true
		}
	}
	return false
}

// Passed counts the checks that held.
func Passed(checks []Check) int {
	var n int
	for _, c := range checks {
		if c.OK {
			n++
		}
	}
	return n
}
