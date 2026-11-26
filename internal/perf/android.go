package perf

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// AndroidSampler measures one app on one Android device over adb, the way
// PerfDog measures a game: frame rate from SurfaceFlinger's own latencies
// (falling back to HWUI's gfxinfo counters), CPU and memory from
// /proc/<pid>, temperature from the battery sensor.
//
// Limits worth knowing: a game drawn in a SurfaceView is measured from its
// own layer; an app that renders nothing in an interval reports 0 fps; the
// temperature is the battery's, not the SoC's.
type AndroidSampler struct {
	ADB    ADB
	Serial string
	Pkg    string

	pid        int
	layer      string
	prevAt     time.Time
	prevCPU    float64
	prevFrames float64
	prevJank   float64
	lastFPS    string
}

// NewAndroidSampler returns a sampler for Pkg on Serial.
func NewAndroidSampler(adb ADB, serial, pkg string) *AndroidSampler {
	return &AndroidSampler{ADB: adb, Serial: serial, Pkg: pkg}
}

// Name is the sampler's label in the UI and the report.
func (s *AndroidSampler) Name() string { return "Android " + s.Serial + " " + s.Pkg }

// Start checks the device and the app and resets the frame counters so the
// first interval is not the app's whole uptime.
func (s *AndroidSampler) Start(ctx context.Context) error {
	if s.Serial == "" {
		return fmt.Errorf("未选择设备")
	}
	if s.Pkg == "" {
		return fmt.Errorf("未填写包名")
	}
	if _, err := s.ADB.Shell(ctx, s.Serial, "echo qawork"); err != nil {
		return err
	}
	pid, err := s.findPID(ctx)
	if err != nil {
		return err
	}
	s.pid = pid
	s.ADB.Shell(ctx, s.Serial, "dumpsys gfxinfo "+s.Pkg+" reset")
	stat, err := s.readStat(ctx)
	if err == nil {
		s.prevCPU = stat.CPUTicks
		s.prevFrames, s.prevJank = s.readGfx(ctx)
	}
	s.prevAt = time.Now()
	return nil
}

// Stop releases nothing; the sampler keeps no background work.
func (s *AndroidSampler) Stop() error { return nil }

func (s *AndroidSampler) findPID(ctx context.Context) (int, error) {
	out, err := s.ADB.Shell(ctx, s.Serial, "pidof "+s.Pkg)
	if err != nil {
		return 0, fmt.Errorf("读取进程 %s: %w", s.Pkg, err)
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return 0, fmt.Errorf("应用 %s 未运行", s.Pkg)
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, fmt.Errorf("进程号 %q 无效", fields[0])
	}
	return pid, nil
}

type procStat struct {
	CPUTicks float64
	Threads  int
	RSSPages int64
}

func (s *AndroidSampler) readStat(ctx context.Context) (procStat, error) {
	out, err := s.ADB.Shell(ctx, s.Serial, fmt.Sprintf("cat /proc/%d/stat", s.pid))
	if err != nil {
		s.pid = 0
		return procStat{}, fmt.Errorf("读取进程状态: %w", err)
	}
	return ParseProcStat(out)
}

// ParseProcStat reads /proc/<pid>/stat, whose comm field may contain spaces
// and parentheses, so the fields after the last ')' are the ones parsed.
func ParseProcStat(line string) (procStat, error) {
	end := strings.LastIndex(line, ")")
	if end < 0 || end+2 >= len(line) {
		return procStat{}, fmt.Errorf("/proc/stat 格式无效")
	}
	fields := strings.Fields(line[end+1:])
	// fields[0] is the state (field 3 of stat): utime is field 14, stime 15,
	// num_threads 20, rss 24.
	if len(fields) < 22 {
		return procStat{}, fmt.Errorf("/proc/stat 字段不足: %d", len(fields))
	}
	utime, err1 := strconv.ParseFloat(fields[11], 64)
	stime, err2 := strconv.ParseFloat(fields[12], 64)
	threads, err3 := strconv.Atoi(fields[17])
	rss, err4 := strconv.ParseInt(fields[21], 10, 64)
	if err := firstErr(err1, err2, err3, err4); err != nil {
		return procStat{}, err
	}
	return procStat{CPUTicks: utime + stime, Threads: threads, RSSPages: rss}, nil
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// frameRate reads SurfaceFlinger's latency buffer for the app's layer; ok
// is false when the layer is gone, as between activities.
func (s *AndroidSampler) frameRate(ctx context.Context) (fps float64, note string, ok bool) {
	if s.layer == "" {
		s.layer = PickLayer(s.listLayers(ctx), s.Pkg)
	}
	if s.layer == "" {
		return 0, "", false
	}
	out, err := s.ADB.Shell(ctx, s.Serial, "dumpsys SurfaceFlinger --latency "+quoteShell(s.layer))
	if err != nil {
		s.layer = ""
		return 0, "", false
	}
	latencyFPS, ok := ParseLatencyFPS(out)
	if !ok {
		s.layer = ""
		return 0, "", false
	}
	return latencyFPS, "SurfaceFlinger", true
}

func (s *AndroidSampler) listLayers(ctx context.Context) []string {
	out, err := s.ADB.Shell(ctx, s.Serial, "dumpsys SurfaceFlinger --list")
	if err != nil {
		return nil
	}
	return strings.Split(out, "\n")
}

// PickLayer chooses the layer whose frames represent the app: the
// SurfaceView a game draws into when there is one, else the app's window.
func PickLayer(layers []string, pkg string) string {
	var window string
	for _, raw := range layers {
		line := strings.TrimSpace(raw)
		if line == "" || !strings.Contains(line, pkg) {
			continue
		}
		if strings.Contains(line, "Background for") || strings.Contains(line, "ActivityRecord") {
			continue
		}
		if strings.Contains(line, "SurfaceView[") {
			return line
		}
		if window == "" {
			window = line
		}
	}
	return window
}

// ParseLatencyFPS reads "dumpsys SurfaceFlinger --latency" output: a header
// of the refresh period, then 127 rows of desired, actual and ready
// timestamps in nanoseconds. Rows with no actual presentation are skipped.
func ParseLatencyFPS(out string) (float64, bool) {
	var stamps []int64
	for i, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if i == 0 && !strings.Contains(line, " ") {
			continue // refresh period header
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		actual, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || actual <= 0 || actual >= math.MaxInt64/2 {
			continue
		}
		stamps = append(stamps, actual)
	}
	if len(stamps) < 4 {
		return 0, false
	}
	span := float64(stamps[len(stamps)-1]-stamps[0]) / 1e9
	if span <= 0 {
		return 0, false
	}
	fps := float64(len(stamps)-1) / span
	if fps <= 0 || fps > 1000 {
		return 0, false
	}
	return fps, true
}

func (s *AndroidSampler) readGfx(ctx context.Context) (frames, jank float64) {
	out, err := s.ADB.Shell(ctx, s.Serial, "dumpsys gfxinfo "+s.Pkg)
	if err != nil {
		return 0, 0
	}
	return ParseGfxInfo(out)
}

// ParseGfxInfo reads HWUI's totals: "Total frames rendered: N" and
// "Janky frames: N (P%)".
func ParseGfxInfo(out string) (frames, jank float64) {
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "total frames rendered:") {
			if v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(lower, "total frames rendered:")), 64); err == nil {
				frames = v
			}
		}
		if strings.HasPrefix(lower, "janky frames:") {
			rest := strings.TrimSpace(strings.TrimPrefix(lower, "janky frames:"))
			if i := strings.Index(rest, "("); i > 0 {
				rest = rest[:i]
			}
			if v, err := strconv.ParseFloat(strings.TrimSpace(rest), 64); err == nil {
				jank = v
			}
		}
	}
	return frames, jank
}

func (s *AndroidSampler) temperature(ctx context.Context) float64 {
	out, err := s.ADB.Shell(ctx, s.Serial, "dumpsys battery")
	if err != nil {
		return 0
	}
	return ParseBatteryTemperature(out)
}

// ParseBatteryTemperature reads "temperature: 302", tenths of a degree
// Celsius, from "dumpsys battery".
func ParseBatteryTemperature(out string) float64 {
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(strings.ToLower(line), "temperature:") {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, "temperature:")), 64)
		if err != nil {
			return 0
		}
		return v / 10
	}
	return 0
}

// Sample takes one interval's measurements.
func (s *AndroidSampler) Sample(ctx context.Context) (Sample, error) {
	now := time.Now()
	sample := Sample{At: now}
	if s.pid == 0 {
		pid, err := s.findPID(ctx)
		if err != nil {
			return sample, err
		}
		s.pid = pid
		s.prevAt = time.Time{}
	}
	stat, err := s.readStat(ctx)
	if err != nil {
		return sample, err
	}

	dt := now.Sub(s.prevAt).Seconds()
	cpu := 0.0
	if !s.prevAt.IsZero() && dt > 0.2 {
		delta := stat.CPUTicks - s.prevCPU
		if delta < 0 {
			delta = 0 // the app restarted between intervals
		}
		cpu = delta / 100 / dt * 100 // /proc jiffies run at 100 Hz on Android
	}
	s.prevCPU = stat.CPUTicks

	sample.CPU = clamp(cpu, 0, 100*s.cores(ctx))
	sample.MemMB = float64(stat.RSSPages) * 4096 / (1024 * 1024)
	sample.Threads = stat.Threads

	fps, note, ok := s.frameRate(ctx)
	if !ok {
		frames, jank := s.readGfx(ctx)
		if !s.prevAt.IsZero() && dt > 0.2 && frames >= s.prevFrames {
			df := frames - s.prevFrames
			fps = df / dt
			if df > 0 {
				sample.JankPct = 100 * (jank - s.prevJank) / df
			}
			note = "gfxinfo"
		}
		s.prevFrames, s.prevJank = frames, jank
		if fps < 0 {
			fps = 0
		}
	} else {
		if !s.prevAt.IsZero() && dt > 0.2 {
			frames, jank := s.readGfx(ctx)
			if frames >= s.prevFrames {
				df := frames - s.prevFrames
				if df > 0 {
					sample.JankPct = 100 * (jank - s.prevJank) / df
				}
			}
			s.prevFrames, s.prevJank = frames, jank
		}
	}
	s.prevAt = now
	sample.FPS = round2(fps)
	if fps > 0 {
		sample.FrameMS = round2(1000 / fps)
	}
	sample.JankPct = round2(clamp(sample.JankPct, 0, 100))
	sample.TempC = round2(s.temperature(ctx))
	sample.Note = note
	s.lastFPS = note
	return sample, nil
}

func (s *AndroidSampler) cores(ctx context.Context) float64 {
	out, err := s.ADB.Shell(ctx, s.Serial, "nproc")
	if err != nil {
		return 8
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(out), 64)
	if err != nil || n <= 0 {
		return 8
	}
	return n
}

func quoteShell(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
