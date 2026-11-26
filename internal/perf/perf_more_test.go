package perf

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseLatencyStamps(t *testing.T) {
	out := "16666666\n" +
		"1 1000000 0\n" +
		"2 17666666 0\n" +
		"bad row\n" +
		"3 0 0\n" +
		"4 34333332 0\n" +
		"5 51000000 0\n"
	stamps := ParseLatencyStamps(out)
	if len(stamps) != 4 {
		t.Fatalf("stamps = %v", stamps)
	}
	if stamps[0] != 1000000 || stamps[3] != 51000000 {
		t.Errorf("stamps = %v", stamps)
	}
	if _, _, ok := ParseLatency(out); !ok {
		t.Error("ParseLatency should accept four presented frames")
	}
}

func TestParseLatencyTooFew(t *testing.T) {
	out := "16666666\n1 1000000 0\n2 17666666 0\n"
	if _, _, ok := ParseLatency(out); ok {
		t.Error("want not ok with fewer than four rows")
	}
	if got := ParseLatencyStamps(""); len(got) != 0 {
		t.Errorf("empty = %v", got)
	}
}

func TestParseGfxInfoMissing(t *testing.T) {
	frames, jank := ParseGfxInfo("no counters here")
	if frames != 0 || jank != 0 {
		t.Errorf("frames=%v jank=%v", frames, jank)
	}
}

func TestParseBatteryTemperatureMissing(t *testing.T) {
	if v := ParseBatteryTemperature("AC powered: false\n"); v != 0 {
		t.Errorf("temp = %v", v)
	}
	if v := ParseBatteryTemperature("temperature: -1\n"); v != 0 {
		t.Errorf("negative temp = %v", v)
	}
}

func TestThreadsForEngines(t *testing.T) {
	if th := EngineUnity.Threads(); th.Main != "UnityMain" || th.Render != "UnityGfxDeviceW" {
		t.Errorf("unity = %+v", th)
	}
	if th := EngineUE.Threads(); th.Main != "GameThread" || th.Render != "RenderThread" {
		t.Errorf("ue = %+v", th)
	}
	if th := EngineNative.Threads(); th.Main != "" || th.Render != "" {
		t.Errorf("native = %+v", th)
	}
	if got := Engine("weird").Label(); got != "未识别" {
		t.Errorf("label = %q", got)
	}
}

func TestEngineDetectionEdges(t *testing.T) {
	cases := []struct {
		names []string
		want  Engine
	}{
		{[]string{"main", "RenderThread"}, EngineNative}, // RenderThread without GameThread is not UE
		{[]string{"GameThread"}, EngineUE},
		{[]string{"GameThread", "RHIThread"}, EngineUE},
		{[]string{"UnityMain"}, EngineUnity},
		{[]string{"UnityGfxDeviceW"}, EngineUnity},
		{[]string{"yGfxDeviceW"}, EngineUnity}, // right-truncated comm
		{[]string{}, EngineNative},
	}
	for _, c := range cases {
		if got := DetectEngine(c.names); got != c.want {
			t.Errorf("DetectEngine(%v) = %v, want %v", c.names, got, c.want)
		}
	}
	if matchName("main", "UnityMain") {
		t.Error("main must not match UnityMain")
	}
	if !matchName("UnityMai", "UnityMain") {
		t.Error("truncated UnityMai should match UnityMain")
	}
}

func TestKeyThreadLongestNameWins(t *testing.T) {
	stats := []threadStat{
		{TID: 1, Name: "Render", Ticks: 1},
		{TID: 2, Name: "RenderThread", Ticks: 2},
	}
	st, ok := keyThread(stats, "RenderThread")
	if !ok || st.TID != 2 {
		t.Errorf("key = %+v ok=%v", st, ok)
	}
	if _, ok := keyThread(stats, ""); ok {
		t.Error("empty want must not match")
	}
	if got := namesOf(stats); len(got) != 2 || got[0] != "Render" {
		t.Errorf("names = %v", got)
	}
}

type fakeSampler struct {
	startErr error
	stopped  bool
	started  bool
	n        int
}

func (f *fakeSampler) Name() string { return "fake" }

func (f *fakeSampler) Start(ctx context.Context) error {
	f.started = true
	return f.startErr
}

func (f *fakeSampler) Sample(ctx context.Context) (Sample, error) {
	f.n++
	return Sample{At: time.Now(), FPS: 60, FrameMS: 16.6, CPU: 10, MemMB: 100, TempC: 36}, nil
}

func (f *fakeSampler) Stop() error {
	f.stopped = true
	return nil
}

func TestRunCapture(t *testing.T) {
	f := &fakeSampler{}
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	samples, err := RunCapture(ctx, f, 200*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !f.started || !f.stopped {
		t.Errorf("started=%v stopped=%v", f.started, f.stopped)
	}
	if len(samples) < 2 {
		t.Errorf("samples = %d, want at least 2", len(samples))
	}
}

func TestRunCaptureClampsInterval(t *testing.T) {
	f := &fakeSampler{}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	samples, err := RunCapture(ctx, f, 10*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) > 4 {
		t.Errorf("samples = %d, interval should be clamped to 200ms", len(samples))
	}
}

func TestRunCaptureStartError(t *testing.T) {
	f := &fakeSampler{startErr: errors.New("boom")}
	if _, err := RunCapture(context.Background(), f, time.Second, nil); err == nil {
		t.Fatal("want start error")
	}
	if f.stopped {
		t.Error("Stop must not run when Start failed")
	}
}

func TestCollector(t *testing.T) {
	f := &fakeSampler{}
	c := NewCollector(f, 10*time.Millisecond) // clamped to 200ms
	var seen int
	c.OnSample(func(Sample) {
		seen++
	})
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	go c.Run(ctx)
	time.Sleep(500 * time.Millisecond)
	cancel()
	time.Sleep(150 * time.Millisecond)
	if got := len(c.Snapshot()); got < 1 {
		t.Errorf("snapshot = %d", got)
	}
	if seen < 1 {
		t.Errorf("onSample = %d", seen)
	}
	if err := c.Err(); err != nil {
		t.Errorf("err = %v", err)
	}
	if c.Sampler() != f {
		t.Error("Sampler mismatch")
	}
}

func TestSummarizeEmptyAndPercentiles(t *testing.T) {
	s := Summarize(nil)
	if s.Samples != 0 || s.FPSMean != 0 || s.FrameP95 != 0 {
		t.Errorf("empty summary = %+v", s)
	}
	if percentile(nil, 0.95) != 0 {
		t.Error("empty percentile")
	}
	if percentile([]float64{1, 2, 3}, 0) != 1 || percentile([]float64{1, 2, 3}, 1) != 3 {
		t.Error("percentile edges")
	}
	if percentile([]float64{1, 2, 3}, 0.5) != 2 {
		t.Error("median")
	}
}

func TestSummarizeEngineFields(t *testing.T) {
	base := time.Now()
	samples := []Sample{
		{At: base, FPS: 60, FrameMS: 16.6, CPU: 20, Engine: EngineUnity, MainThread: "UnityMain", RenderThread: "UnityGfxDeviceW", MainCPU: 10, RenderCPU: 8},
		{At: base.Add(time.Second), FPS: 30, FrameMS: 33.3, CPU: 40, Engine: EngineUnity, MainThread: "UnityMain", RenderThread: "UnityGfxDeviceW", MainCPU: 50, RenderCPU: 30},
	}
	s := Summarize(samples)
	if s.Engine != EngineUnity || s.EngineLabel != "Unity" {
		t.Errorf("engine = %v %q", s.Engine, s.EngineLabel)
	}
	if s.MainThread != "UnityMain" || s.MainCPUMax != 50 || s.RenderCPUMax != 30 {
		t.Errorf("threads = %+v", s)
	}
}

func TestEvaluateSkipsZeroLimits(t *testing.T) {
	tk := Thresholds{}
	if checks := tk.Evaluate(Summary{FPSMean: 1, FrameSamples: 2}); len(checks) != 0 {
		t.Errorf("checks = %+v", checks)
	}
	if Failed(nil) {
		t.Error("no checks must pass")
	}
	if Passed(nil) != 0 {
		t.Error("passed count")
	}
}

func TestEvaluateWithoutFrames(t *testing.T) {
	tk := DefaultThresholds()
	s := Summary{CPUMax: 20, MemMaxMB: 100, TempMax: 30, FrameSamples: 0}
	checks := tk.Evaluate(s)
	for _, c := range checks {
		if c.Name == "平均帧率" || c.Name == "卡顿帧占比" {
			t.Errorf("frame check should be skipped: %+v", c)
		}
	}
	if Failed(checks) {
		t.Error("checks should pass")
	}
}

func TestCSVLegacy(t *testing.T) {
	legacy := "at,fps,frame_ms,jank_pct,cpu_pct,mem_mb,temp_c,threads,note\n" +
		"2026-11-26T10:00:00+08:00,60,16.6,1,20,100,36,24,SurfaceFlinger\n"
	got, err := ReadCSV(strings.NewReader(legacy))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Note != "SurfaceFlinger" || got[0].Engine != EngineUnknown {
		t.Errorf("legacy = %+v", got)
	}
}

func TestCSVErrors(t *testing.T) {
	if _, err := ReadCSV(strings.NewReader("")); err == nil {
		t.Error("empty CSV must error")
	}
	if _, err := ReadCSV(strings.NewReader("at,fps\n1,2,3,4,5\n")); err == nil {
		t.Error("short row must error")
	}
	if _, err := ReadCSV(strings.NewReader("at,fps,frame_ms,jank_pct,cpu_pct,mem_mb,temp_c,threads\nnot-a-time,1,2,3,4,5,6,7\n")); err == nil {
		t.Error("bad timestamp must error")
	}
	if _, err := LoadCSV("Z:\\nope\\missing.csv"); err == nil {
		t.Error("missing file must error")
	}
}

func TestSaveAndLoadCSV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.csv")
	samples := []Sample{{At: time.Now(), FPS: 50, FrameMS: 20, CPU: 10, MemMB: 100, TempC: 35, Threads: 10, Note: "gfxinfo"}}
	if err := SaveCSV(path, samples); err != nil {
		t.Fatal(err)
	}
	got, err := LoadCSV(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].FPS != 50 || got[0].Note != "gfxinfo" {
		t.Errorf("got = %+v", got)
	}
}

func TestWriteCSVHeader(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteCSV(&buf, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "engine,main_thread") {
		t.Errorf("header = %q", buf.String())
	}
}

func TestParseDevicesEmptyAndLabelFallback(t *testing.T) {
	if got := ParseDevices("List of devices attached\n"); len(got) != 0 {
		t.Errorf("devices = %v", got)
	}
	d := Device{Serial: "abc"}
	if d.Label() != "abc" {
		t.Errorf("label = %q", d.Label())
	}
}

func TestRoundAndClamp(t *testing.T) {
	if round2(1.005) != 1.01 && round2(1.005) != 1.0 {
		// float representation: just require two decimals
		t.Errorf("round2 = %v", round2(1.005))
	}
	if clamp(-1, 0, 100) != 0 || clamp(200, 0, 100) != 100 || clamp(50, 0, 100) != 50 {
		t.Error("clamp")
	}
}
