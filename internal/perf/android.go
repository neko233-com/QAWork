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

	engine     Engine
	threadPrev map[int]float64
	samples    int
	lastStamp  int64
	lastTemp   float64
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
	s.engine = DetectEngine(namesOf(s.readThreads(ctx)))
	s.threadPrev = make(map[int]float64)
	s.ADB.Shell(ctx, s.Serial, "dumpsys gfxinfo "+s.Pkg+" reset")
	stat, err := s.readStat(ctx)
	if err == nil {
		s.prevCPU = stat.CPUTicks
		s.prevFrames, s.prevJank = s.readGfx(ctx)
	}
	// Prime the latency buffer: the first interval must see it advance,
	// otherwise the pre-start frames would pass for the current rate.
	s.layer = PickLayer(s.listLayers(ctx), s.Pkg)
	if s.layer != "" {
		out, latErr := s.ADB.Shell(ctx, s.Serial, "dumpsys SurfaceFlinger --latency "+quoteShell(s.layer))
		if latErr == nil {
			if _, newest, ok := ParseLatency(out); ok {
				s.lastStamp = newest
			}
		}
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

// frameRate counts the frames SurfaceFlinger presented for the app's layer
// since the last read, over the host interval: real hit rate, including
// gaps where the app rendered nothing. ok is false when the layer is gone
// or the buffer did not advance.
func (s *AndroidSampler) frameRate(ctx context.Context, dt float64) (fps float64, note string, ok bool) {
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
	stamps := ParseLatencyStamps(out)
	if len(stamps) < 2 {
		return 0, "", false
	}
	newest := stamps[len(stamps)-1]
	if s.lastStamp <= 0 || dt <= 0.2 {
		s.lastStamp = newest
		return 0, "", false
	}
	count := 0
	for _, stamp := range stamps {
		if stamp > s.lastStamp {
			count++
		}
	}
	s.lastStamp = newest
	if count == 0 {
		return 0, "", false // nothing new: fall back to gfxinfo
	}
	fps = float64(count) / dt
	if fps > 240 {
		fps = 240 // the buffer holds 127 frames; the cap is documented
	}
	return fps, "SurfaceFlinger", true
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
// Android 12+ lists layers as "RequestedLayerState{<name> parentId=... z=...}",
// so the name is extracted before it is handed to --latency.
func PickLayer(layers []string, pkg string) string {
	var window string
	for _, raw := range layers {
		name := ParseLayerName(raw)
		if name == "" || !strings.Contains(name, pkg) {
			continue
		}
		if strings.Contains(name, "Background for") || strings.Contains(name, "ActivityRecord") {
			continue
		}
		if strings.Contains(name, "SurfaceView[") {
			return name
		}
		if window == "" {
			window = name
		}
	}
	return window
}

// ParseLayerName strips the RequestedLayerState wrapper and the attributes
// after the layer's own name.
func ParseLayerName(line string) string {
	s := strings.TrimSpace(line)
	if i := strings.IndexByte(s, '{'); i >= 0 {
		if j := strings.LastIndexByte(s, '}'); j > i {
			s = s[i+1 : j]
		}
	}
	cut := len(s)
	for _, marker := range []string{" parentId=", " relativeParentId=", " z="} {
		if i := strings.Index(s, marker); i > 0 && i < cut {
			cut = i
		}
	}
	return strings.TrimSpace(s[:cut])
}

// ParseLatencyFPS reads "dumpsys SurfaceFlinger --latency" output and
// returns the frame rate over the timestamps it holds.
func ParseLatencyFPS(out string) (float64, bool) {
	fps, _, ok := ParseLatency(out)
	return fps, ok
}

// ParseLatency reads the latency buffer: a header of the refresh period,
// then rows of desired, actual and ready timestamps in nanoseconds. It
// returns the rate over the buffer and the newest presented timestamp, so
// a caller can tell whether the buffer advanced since the last read.
func ParseLatency(out string) (fps float64, newest int64, ok bool) {
	stamps := ParseLatencyStamps(out)
	if len(stamps) < 4 {
		return 0, 0, false
	}
	span := float64(stamps[len(stamps)-1]-stamps[0]) / 1e9
	if span <= 0 {
		return 0, 0, false
	}
	fps = float64(len(stamps)-1) / span
	if fps <= 0 || fps > 1000 {
		return 0, 0, false
	}
	return fps, stamps[len(stamps)-1], true
}

// ParseLatencyStamps returns the presented timestamps of the buffer, oldest
// first. Counting the stamps newer than the last read over the host's
// interval measures the frame rate the app actually hit, including the
// intervals where it rendered nothing.
func ParseLatencyStamps(out string) []int64 {
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
	return stamps
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
		return math.Max(0, v/10)
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
		s.engine = EngineUnknown
		s.threadPrev = nil
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

	// Per-engine threads: Unity's main and GPU device thread, UE's game and
	// render thread. Names come from the process itself, so release builds
	// need no hook and no debuggable APK.
	threads := s.readThreads(ctx)
	s.samples++
	if s.engine == EngineUnknown || s.samples%10 == 0 {
		if detected := DetectEngine(namesOf(threads)); detected != EngineNative || s.engine == EngineUnknown {
			s.engine = detected
		}
	}
	s.fillThreadCPU(threads, &sample, dt)
	if s.engine != EngineUnknown {
		sample.Engine = s.engine
	}

	fps, note, ok := s.frameRate(ctx, dt)
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
		if fps <= 0 && note == "" {
			note = "无新帧"
		}
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
	temp := s.temperature(ctx)
	if temp > 0 {
		s.lastTemp = temp
	} else {
		temp = s.lastTemp
	}
	sample.TempC = round2(temp)
	sample.Note = note
	s.lastFPS = note
	return sample, nil
}

// readThreads lists the target's threads and their cumulative CPU ticks.
// /proc/<pid>/task is readable for other apps on Android; the CPU tick
// delta turns into a per-thread share that names the engine's key threads.
func (s *AndroidSampler) readThreads(ctx context.Context) []threadStat {
	cmd := fmt.Sprintf("for t in /proc/%d/task/*; do echo @@$(cat $t/comm)@@$(cat $t/stat); done", s.pid)
	out, err := s.ADB.Shell(ctx, s.Serial, cmd)
	if err != nil {
		return nil
	}
	return ParseThreadStats(out)
}

// fillThreadCPU computes the interval's CPU share for the engine's main and
// render threads, and labels the sample with what it found.
func (s *AndroidSampler) fillThreadCPU(stats []threadStat, sample *Sample, dt float64) {
	if s.threadPrev == nil {
		s.threadPrev = make(map[int]float64)
	}
	cpus := make(map[int]float64, len(stats))
	for _, st := range stats {
		delta := 0.0
		if prev, ok := s.threadPrev[st.TID]; ok && dt > 0.2 {
			delta = st.Ticks - prev
			if delta < 0 {
				delta = 0
			}
		}
		s.threadPrev[st.TID] = st.Ticks
		if dt > 0.2 {
			cpus[st.TID] = clamp(delta/100/dt*100, 0, 100)
		}
	}
	key := s.engine.Threads()
	if key.Main != "" {
		if st, ok := keyThread(stats, key.Main); ok {
			sample.MainThread = key.Main
			sample.MainCPU = round2(cpus[st.TID])
		}
	}
	if key.Render != "" {
		if st, ok := keyThread(stats, key.Render); ok {
			sample.RenderThread = key.Render
			sample.RenderCPU = round2(cpus[st.TID])
		}
	}
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
