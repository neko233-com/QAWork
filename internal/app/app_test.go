package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neko233-com/QAWork/internal/automation"
	"github.com/neko233-com/QAWork/internal/compat"
	"github.com/neko233-com/QAWork/internal/perf"
	"github.com/neko233-com/QAWork/internal/setup"
)

func TestNewDefaults(t *testing.T) {
	a := New(nil)
	if a.Mode != ModePerfDog {
		t.Errorf("mode = %d, default must be PerfDog", a.Mode)
	}
	if a.IntervalLabel != "1 s" || a.NominalFPS != 60 {
		t.Errorf("interval=%q nominal=%v", a.IntervalLabel, a.NominalFPS)
	}
	if a.ShowSetup {
		t.Error("setup must not open by default")
	}
	if a.Thresholds != perf.DefaultThresholds() {
		t.Errorf("thresholds = %+v", a.Thresholds)
	}
	if a.automationPlanPath != "qawork.yaml" {
		t.Errorf("plan path = %q", a.automationPlanPath)
	}
}

func TestAutomationStartGuards(t *testing.T) {
	a := New(nil)
	a.Target = "com.example.game"
	a.Serial = "s"
	a.mu.Lock()
	a.automationRunning = true
	a.mu.Unlock()
	a.Start()
	if !strings.Contains(a.Status(), "自动化") {
		t.Errorf("status = %q", a.Status())
	}
	if a.IsRunning() {
		t.Error("perf capture must not start while automation runs")
	}
}

func TestValidatePlanPanel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "qawork.yaml")
	if err := os.WriteFile(path, []byte("version: 1\ncases:\n  - name: a\n    package: com.a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := New(nil)
	a.automationPlanPath = path
	a.ValidatePlan()
	if !strings.Contains(a.automationMsg, "计划有效") {
		t.Errorf("msg = %q", a.automationMsg)
	}

	a.automationPlanPath = filepath.Join(dir, "missing.yaml")
	a.ValidatePlan()
	if !strings.Contains(a.automationMsg, "读取失败") {
		t.Errorf("msg = %q", a.automationMsg)
	}
}

func TestRunPlanRejectsLocalTarget(t *testing.T) {
	a := New(nil)
	a.Serial = "local"
	a.RunPlan()
	if a.automationRunning {
		t.Error("must not start")
	}
	if !strings.Contains(a.automationMsg, "只支持 Android") {
		t.Errorf("msg = %q", a.automationMsg)
	}
}

func TestRunCompatRequiresPackageWithoutPlan(t *testing.T) {
	a := New(nil)
	a.compatPlanPath = ""
	a.compatPackage = ""
	a.RunCompat()
	if a.compatRunning {
		t.Error("must not start")
	}
	if !strings.Contains(a.compatMsg, "包名") {
		t.Errorf("msg = %q", a.compatMsg)
	}
}

func TestPanelStateCopies(t *testing.T) {
	a := New(nil)
	a.mu.Lock()
	a.automationResults = []automation.Result{{Case: "a"}}
	a.automationMsg = ""
	a.compatMatrix = compat.Matrix{Rows: []compat.Row{{DeviceInfo: compat.DeviceInfo{Serial: "s"}}}}
	a.mu.Unlock()

	results, _, running := a.AutomationState()
	if len(results) != 1 || running {
		t.Fatalf("automation = %+v", results)
	}
	results[0].Case = "mutated"
	if got, _, _ := a.AutomationState(); got[0].Case != "a" {
		t.Error("automation results must be copied")
	}
	if got := a.AutomationNotice(); !strings.Contains(got, "qawork.yaml") {
		t.Errorf("notice = %q", got)
	}

	m, _, _ := a.CompatState()
	if len(m.Rows) != 1 {
		t.Fatalf("compat = %+v", m)
	}
	m.Rows[0].Serial = "mutated"
	if got, _, _ := a.CompatState(); got.Rows[0].Serial != "s" {
		t.Error("compat rows must be copied")
	}
	if got := a.CompatNotice(); !strings.Contains(got, "在线设备") {
		t.Errorf("notice = %q", got)
	}
}

func TestCompatFormatHelpers(t *testing.T) {
	if yesNo(true) != "是" || yesNo(false) != "否" {
		t.Error("yesNo")
	}
	if msText(0) != "—" || msText(123.4) != "123ms" {
		t.Errorf("msText = %q %q", msText(0), msText(123.4))
	}
	if fpsText(0) != "—" || fpsText(30.5) != "30.5" {
		t.Errorf("fpsText = %q %q", fpsText(0), fpsText(30.5))
	}
}

func TestInterval(t *testing.T) {
	a := New(nil)
	cases := map[string]time.Duration{
		"500 ms": 500 * time.Millisecond,
		"1 s":    time.Second,
		"2 s":    2 * time.Second,
		"weird":  time.Second,
	}
	for label, want := range cases {
		a.IntervalLabel = label
		if got := a.interval(); got != want {
			t.Errorf("interval(%q) = %v, want %v", label, got, want)
		}
	}
}

func TestSessionName(t *testing.T) {
	got := sessionName("127.0.0.1:16384", "com.example/game")
	if !strings.HasSuffix(got, ".csv") {
		t.Errorf("name = %q", got)
	}
	if strings.ContainsAny(got, ":/\\ ") {
		t.Errorf("name still has path separators: %q", got)
	}
	if !strings.HasPrefix(got, "127.0.0.1_16384-com.example_game-") {
		t.Errorf("name = %q", got)
	}
}

func TestDataDirHonoursHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QAWORK_HOME", home)
	if got := DataDir(); got != home {
		t.Errorf("DataDir = %q, want %q", got, home)
	}
}

func TestBarHeight(t *testing.T) {
	if got := barHeight(30, 0); got != 2 {
		t.Errorf("barHeight with no max = %v", got)
	}
	if got := barHeight(60, 60); got < 86 {
		t.Errorf("full bar = %v", got)
	}
	if got := barHeight(120, 60); got > 88 {
		t.Errorf("clamped bar = %v", got)
	}
}

func TestSeriesWindows(t *testing.T) {
	samples := []perf.Sample{
		{FPS: 1, CPU: 10}, {FPS: 2, CPU: 20}, {FPS: 3, CPU: 30}, {FPS: 4, CPU: 40},
	}
	fps := fpsSeries(samples, 2)
	if len(fps) != 2 || fps[0] != 3 || fps[1] != 4 {
		t.Errorf("fps = %v", fps)
	}
	cpu := cpuSeries(samples, 2)
	if len(cpu) != 2 || cpu[1] != 40 {
		t.Errorf("cpu = %v", cpu)
	}
	if got := fpsSeries(samples, 10); len(got) != 4 {
		t.Errorf("all = %v", got)
	}
}

func TestStartRequiresTarget(t *testing.T) {
	a := New(nil)
	a.Start()
	if !strings.Contains(a.Status(), "先填目标") {
		t.Errorf("status = %q", a.Status())
	}
	if a.IsRunning() {
		t.Error("must not run without a target")
	}
}

func TestStartRequiresDevice(t *testing.T) {
	a := New(nil)
	a.Target = "com.example.game"
	a.Start()
	if !strings.Contains(a.Status(), "先选设备") {
		t.Errorf("status = %q", a.Status())
	}
}

func TestSetupStateInitiallyEmpty(t *testing.T) {
	a := New(nil)
	deps, dir, msg, running := a.SetupState()
	if len(deps) != 0 || dir != "" || msg != "" || running {
		t.Errorf("state = %v %q %q %v", deps, dir, msg, running)
	}
}

func TestSnapshotAndSummary(t *testing.T) {
	a := New(nil)
	a.mu.Lock()
	a.samples = []perf.Sample{
		{At: time.Now(), FPS: 60, FrameMS: 16.6, CPU: 20, MemMB: 100, TempC: 36, Threads: 20},
		{At: time.Now(), FPS: 40, FrameMS: 25, CPU: 40, MemMB: 120, TempC: 37, Threads: 20},
	}
	a.mu.Unlock()
	snap := a.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot = %v", snap)
	}
	snap[0].FPS = 0 // must not mutate the app's copy
	if got := a.Snapshot()[0].FPS; got != 60 {
		t.Errorf("app sample mutated = %v", got)
	}
	s := a.Summary()
	if s.FPSMean != 50 || s.CPUMax != 40 || s.Samples != 2 {
		t.Errorf("summary = %+v", s)
	}
	if last, ok := a.Latest(); !ok || last.FPS != 40 {
		t.Errorf("latest = %+v ok=%v", last, ok)
	}
}

func TestRefreshDevicesWithoutADB(t *testing.T) {
	a := New(nil)
	a.ADB.Path = filepath.Join(t.TempDir(), "missing-adb.exe")
	a.RefreshDevices(context.Background())
	devices, errText := a.Devices()
	if len(devices) != 1 || devices[0].Serial != "local" {
		t.Errorf("devices = %+v", devices)
	}
	if !strings.Contains(errText, "adb 不可用") {
		t.Errorf("error = %q", errText)
	}
}

func TestInstallRequiredNoopWhenNothingMissing(t *testing.T) {
	a := New(nil)
	a.mu.Lock()
	a.setupDeps = []setup.Dep{{ID: "adb", Required: true, Status: setup.StatusOK}}
	a.mu.Unlock()
	a.InstallRequired()
	if running := func() bool { _, _, _, r := a.SetupState(); return r }(); running {
		t.Error("InstallRequired must not start an install when adb is present")
	}
}

func TestRefreshSetupAdoptsADB(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QAWORK_HOME", home)
	adbDir := filepath.Join(home, "tools", "platform-tools")
	if err := os.MkdirAll(adbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(adbDir, "adb.exe"), []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(adbDir, "adb"), []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	a := New(nil)
	a.RefreshSetup(context.Background())
	if a.ADB.Path == "" {
		t.Error("ADB path not adopted from bundled tools")
	}
	deps, dir, _, _ := a.SetupState()
	if dir == "" || len(deps) == 0 {
		t.Errorf("setup state = %v %q", deps, dir)
	}
}
