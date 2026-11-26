package perf

import (
	"bytes"
	"testing"
	"time"
)

func TestParseProcStat(t *testing.T) {
	// comm contains a space and parentheses, the case a naive split breaks.
	line := "1234 (com.example Game) S 1 2 3 4 5 6 7 8 9 10 120 45 11 12 0 -20 24 13 0 1000 900 2000"
	st, err := ParseProcStat(line)
	if err != nil {
		t.Fatal(err)
	}
	// utime 120, stime 45
	if st.CPUTicks != 165 {
		t.Errorf("CPUTicks = %v, want 165", st.CPUTicks)
	}
	// num_threads is field 20: index 17 of the fields after ")", which is 24 here.
	if st.Threads != 24 {
		t.Errorf("Threads = %v, want 24", st.Threads)
	}
	// rss is field 24: index 21, which is 900 pages here.
	if st.RSSPages != 900 {
		t.Errorf("RSSPages = %v, want 900", st.RSSPages)
	}
}

func TestParseProcStatInvalid(t *testing.T) {
	if _, err := ParseProcStat("garbage"); err == nil {
		t.Fatal("want error")
	}
	if _, err := ParseProcStat("1 (x) S 1 2"); err == nil {
		t.Fatal("want error for short line")
	}
}

func TestParseLatencyFPS(t *testing.T) {
	out := "16666666\n" +
		"1 1000000 0\n" +
		"2 17666666 0\n" +
		"3 34333332 0\n" +
		"4 51000000 0\n" +
		"5 67666666 0\n"
	fps, ok := ParseLatencyFPS(out)
	if !ok {
		t.Fatal("want fps")
	}
	// Four intervals of 16.666 ms span 66.666 ms: 4 frames in 66.7 ms ≈ 60 fps.
	if fps < 59 || fps > 61 {
		t.Errorf("fps = %v, want ≈60", fps)
	}
}

func TestParseLatencyEmptyRows(t *testing.T) {
	out := "16666666\n" +
		"1 0 0\n2 0 0\n3 0 0\n"
	if _, ok := ParseLatencyFPS(out); ok {
		t.Fatal("want no fps when nothing was presented")
	}
}

func TestParseGfxInfo(t *testing.T) {
	out := "Stats since: 1234ns\nTotal frames rendered: 500\nJanky frames: 25 (5.00%)\n"
	frames, jank := ParseGfxInfo(out)
	if frames != 500 || jank != 25 {
		t.Errorf("frames=%v jank=%v, want 500/25", frames, jank)
	}
}

func TestParseBatteryTemperature(t *testing.T) {
	if v := ParseBatteryTemperature("AC powered: false\ntemperature: 302\n"); v != 30.2 {
		t.Errorf("temp = %v, want 30.2", v)
	}
	if v := ParseBatteryTemperature("temperature: 0\n"); v != 0 {
		t.Errorf("temp = %v, want 0", v)
	}
}

func TestParseDevices(t *testing.T) {
	out := `List of devices attached
R5CT30 device product:beyond1 model:SM_G973F device:beyond1 transport_id:1
emulator-5554 offline
`
	got := ParseDevices(out)
	if len(got) != 2 {
		t.Fatalf("devices = %v", got)
	}
	if got[0].Serial != "R5CT30" || got[0].Model != "SM_G973F" || got[0].State != "device" {
		t.Errorf("first = %+v", got[0])
	}
	if got[0].Label() != "SM G973F" {
		t.Errorf("label = %q", got[0].Label())
	}
	if got[1].Label() != "emulator-5554（offline）" {
		t.Errorf("label = %q", got[1].Label())
	}
}

func TestPickLayer(t *testing.T) {
	layers := []string{
		"Background for com.example.game/com.example.game.MainActivity",
		"com.example.game/com.example.game.MainActivity#0",
		"SurfaceView[com.example.game/com.example.game.MainActivity]#0",
	}
	if got := PickLayer(layers, "com.example.game"); got != "SurfaceView[com.example.game/com.example.game.MainActivity]#0" {
		t.Errorf("layer = %q", got)
	}
	if got := PickLayer([]string{"com.other/app#0"}, "com.example.game"); got != "" {
		t.Errorf("layer = %q, want empty", got)
	}
}

func TestSummarize(t *testing.T) {
	base := time.Now()
	samples := []Sample{
		{At: base, FPS: 60, FrameMS: 16.67, CPU: 20, MemMB: 100, TempC: 30, JankPct: 0},
		{At: base.Add(time.Second), FPS: 30, FrameMS: 33.33, CPU: 40, MemMB: 120, TempC: 31, JankPct: 10},
		{At: base.Add(2 * time.Second), FPS: 0, CPU: 0, MemMB: 0},
		{At: base.Add(3 * time.Second), FPS: 24, FrameMS: 41.67, CPU: 60, MemMB: 140, TempC: 33, JankPct: 20},
	}
	s := Summarize(samples)
	if s.Samples != 4 {
		t.Errorf("samples = %v", s.Samples)
	}
	if s.FPSMean < 37 || s.FPSMean > 39 {
		t.Errorf("FPSMean = %v, want ≈38", s.FPSMean)
	}
	if s.FPSMin != 24 {
		t.Errorf("FPSMin = %v", s.FPSMin)
	}
	if s.CPUMax != 60 || s.MemMaxMB != 140 || s.TempMax != 33 {
		t.Errorf("maxes = %v/%v/%v", s.CPUMax, s.MemMaxMB, s.TempMax)
	}
	if s.FrameP50 < 16.6 || s.FrameP50 > 33.4 {
		t.Errorf("P50 = %v", s.FrameP50)
	}
}

func TestThresholds(t *testing.T) {
	tk := Thresholds{MinFPSMean: 50, MaxJankPct: 5, MaxCPU: 70, MaxMemMB: 1024, MaxTempC: 45}
	s := Summary{FPSMean: 55, FrameSamples: 4, JankPct: 2, CPUMax: 80, MemMaxMB: 900, TempMax: 0}
	checks := tk.Evaluate(s)
	if len(checks) != 4 { // temperature skipped when the device reports none
		t.Fatalf("checks = %v", checks)
	}
	if !Failed(checks) {
		t.Fatal("CPU above the limit should fail")
	}
	if Passed(checks) != 3 {
		t.Errorf("passed = %v", Passed(checks))
	}
}

func TestDropRate(t *testing.T) {
	if v := DropRate(60, 60); v != 0 {
		t.Errorf("drop = %v", v)
	}
	if v := DropRate(30, 60); v != 50 {
		t.Errorf("drop = %v", v)
	}
	if v := DropRate(90, 60); v != 0 {
		t.Errorf("drop above target = %v", v)
	}
}

func TestCSVRoundTrip(t *testing.T) {
	base := time.Now().Truncate(time.Millisecond)
	samples := []Sample{
		{At: base, FPS: 59.5, FrameMS: 16.8, JankPct: 1.2, CPU: 33.3, MemMB: 512.4, TempC: 38.5, Threads: 42, Note: "SurfaceFlinger"},
		{At: base.Add(time.Second), FPS: 0, Note: "idle, 空闲"},
	}
	var buf bytes.Buffer
	if err := WriteCSV(&buf, samples); err != nil {
		t.Fatal(err)
	}
	got, err := ReadCSV(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v samples", len(got))
	}
	if !got[0].At.Equal(samples[0].At) || got[0].FPS != 59.5 || got[0].Threads != 42 || got[0].Note != "SurfaceFlinger" {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].FPS != 0 || got[1].Note != "idle, 空闲" {
		t.Errorf("second = %+v", got[1])
	}
}
