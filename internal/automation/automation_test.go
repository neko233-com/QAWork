package automation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neko233-com/QAWork/internal/perf"
)

func floatPtr(v float64) *float64 { return &v }

func TestParsePlanAndResolveInheritance(t *testing.T) {
	plan, err := ParsePlan([]byte(`
version: 1
defaults:
  interval: 500ms
  warmup: 0s
  settle: 2s
  serial: default-serial
  assert:
    min_fps: 50
    max_cpu: 0
cases:
  - name: one
    package: com.example.game
    warmup: 5s
    assert:
      max_cpu: 60
      max_jank: 0
`))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := plan.Resolve(0)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Serial != "default-serial" {
		t.Errorf("serial = %q", cfg.Serial)
	}
	if cfg.Interval != 500*time.Millisecond {
		t.Errorf("interval = %v", cfg.Interval)
	}
	if cfg.Warmup != 5*time.Second {
		t.Errorf("warmup override = %v", cfg.Warmup)
	}
	if cfg.Settle != 2*time.Second {
		t.Errorf("settle = %v", cfg.Settle)
	}
	if cfg.Limits.Thresholds.MinFPSMean != 50 {
		t.Errorf("min fps = %v", cfg.Limits.Thresholds.MinFPSMean)
	}
	if cfg.Limits.Thresholds.MaxCPU != 60 {
		t.Errorf("max cpu override = %v", cfg.Limits.Thresholds.MaxCPU)
	}
	if cfg.Limits.Thresholds.MaxJankPct != 0 {
		t.Errorf("explicit 0 must disable jank, got %v", cfg.Limits.Thresholds.MaxJankPct)
	}
	if cfg.Limits.MaxFrameP95 != 33 || cfg.Limits.MaxStartupMS != 5000 {
		t.Errorf("limits = %+v", cfg.Limits)
	}
}

func TestParsePlanCollectsAllErrors(t *testing.T) {
	_, err := ParsePlan([]byte(`
version: 2
defaults:
  interval: 100ms
cases:
  - name: dup
    package: com.a
    steps:
      - {action: tap, x: 1, y: 2}
      - {action: launch}
      - {action: wait}
      - {action: text, text: "中文"}
  - name: dup
    package: ""
    assert:
      max_temp_c: -1
`))
	if err == nil {
		t.Fatal("want validation error")
	}
	msg := err.Error()
	for _, want := range []string{
		"version", "defaults.interval", "重复", "package", "launch 只能是第一步",
		"wait 需要", "只支持 ASCII", "max_temp_c 不能为负",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error misses %q:\n%s", want, msg)
		}
	}
}

func TestParsePlanDefaultsVersionAndNames(t *testing.T) {
	plan, err := ParsePlan([]byte("cases:\n  - name: a\n    package: com.a\n"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Version != 1 {
		t.Errorf("version = %d", plan.Version)
	}
	if got := plan.Names(); len(got) != 1 || got[0] != "a" {
		t.Errorf("names = %v", got)
	}
}

func TestSelectIndices(t *testing.T) {
	plan := &Plan{Cases: []Case{{Name: "a"}, {Name: "b"}}}
	all, err := SelectIndices(plan, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("all = %v %v", all, err)
	}
	one, err := SelectIndices(plan, "b")
	if err != nil || len(one) != 1 || one[0] != 1 {
		t.Fatalf("one = %v %v", one, err)
	}
	if _, err := SelectIndices(plan, "missing"); err == nil {
		t.Fatal("want error")
	}
}

func TestParseLauncherActivity(t *testing.T) {
	cases := map[string]string{
		"priority=0\ncom.example/.MainActivity\n": "com.example/.MainActivity",
		"No activity found\n":                     "",
		"":                                        "",
		"  com.example/com.example.Home  \n":      "com.example/com.example.Home",
	}
	for in, want := range cases {
		if got := ParseLauncherActivity(in); got != want {
			t.Errorf("ParseLauncherActivity(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseStartupMS(t *testing.T) {
	total, wait, ok := ParseStartupMS("Status: ok\nTotalTime: 1234\nWaitTime: 1300\n")
	if !ok || total != 1234 || wait != 1300 {
		t.Errorf("startup = %v %v %v", total, wait, ok)
	}
	if _, _, ok := ParseStartupMS("TotalTime: 0\n"); ok {
		t.Error("zero total must not count as measured")
	}
	if _, _, ok := ParseStartupMS("nothing"); ok {
		t.Error("missing total must not count")
	}
}

func TestStepCommand(t *testing.T) {
	cases := []struct {
		step Step
		want string
	}{
		{Step{Action: "tap", X: 100, Y: 200}, "input tap 100 200"},
		{Step{Action: "longpress", X: 10, Y: 20}, "input swipe 10 20 10 20 800"},
		{Step{Action: "swipe", X: 1, Y: 2, X2: 3, Y2: 4}, "input swipe 1 2 3 4 300"},
		{Step{Action: "swipe", X: 1, Y: 2, X2: 3, Y2: 4, DurationMS: 500}, "input swipe 1 2 3 4 500"},
		{Step{Action: "drag", X: 1, Y: 2, X2: 3, Y2: 4}, "input swipe 1 2 3 4 800"},
		{Step{Action: "keyevent", Key: "26"}, "input keyevent '26'"},
		{Step{Action: "text", Text: "hello world"}, "input text 'hello%sworld'"},
		{Step{Action: "wait"}, ""},
		{Step{Action: "stop"}, ""},
		{Step{Action: "assert_package"}, ""},
		{Step{Action: "screenshot"}, ""},
	}
	for _, c := range cases {
		got, err := StepCommand(c.step)
		if err != nil {
			t.Errorf("%s: %v", c.step.Action, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s = %q, want %q", c.step.Action, got, c.want)
		}
	}
	if _, err := StepCommand(Step{Action: "doubletap", X: 1, Y: 2}); err == nil {
		t.Error("doubletap must be handled by the runner")
	}
	if _, err := StepCommand(Step{Action: "bogus"}); err == nil {
		t.Error("unknown action must fail")
	}
}

func TestParsePlanActivityTarget(t *testing.T) {
	plan, err := ParsePlan([]byte(`
version: 1
cases:
  - name: a
    package: com.a
    steps:
      - {action: launch}
      - {action: wait_activity, activity: .Main, timeout_ms: 5000}
      - {action: assert_activity, text: .Main}
`))
	if err != nil {
		t.Fatal(err)
	}
	steps := plan.Cases[0].Steps
	if steps[1].Activity != ".Main" || steps[2].Text != ".Main" {
		t.Errorf("steps = %+v", steps)
	}
	if errs := plan.Validate(); len(errs) > 0 {
		t.Fatal(errs)
	}
}

func TestParsePlanDeepSteps(t *testing.T) {
	plan, err := ParsePlan([]byte(`
version: 1
cases:
  - name: deep
    package: com.example.game
    steps:
      - {action: launch}
      - {action: longpress, x: 10, y: 20, duration_ms: 900}
      - {action: doubletap, x: 30, y: 40}
      - {action: drag, x: 1, y: 2, x2: 3, y2: 4, duration_ms: 700}
      - {action: stop}
      - {action: clear}
      - {action: grant, permissions: [android.permission.CAMERA, android.permission.RECORD_AUDIO]}
      - {action: revoke, permissions: [android.permission.CAMERA]}
      - {action: wait_activity, text: com.example.game/.Main, timeout_ms: 5000}
      - {action: wait_text, text: "开始", timeout_ms: 5000}
      - {action: assert_activity, text: .Main}
      - {action: assert_text, text: "开始"}
      - {action: assert_not_text, text: "崩溃"}
      - {action: assert_package}
      - {action: screenshot}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Cases[0].Steps) != 15 {
		t.Fatalf("steps = %d", len(plan.Cases[0].Steps))
	}
}

func TestValidateRejectsBadDeepSteps(t *testing.T) {
	_, err := ParsePlan([]byte(`
version: 1
cases:
  - name: bad
    package: com.example.game
    steps:
      - {action: grant, permissions: ["bad permission"]}
      - {action: wait_text, text: "", timeout_ms: -1}
      - {action: assert_activity}
      - {action: doubletap, x: -1, y: 2}
      - {action: drag, x: 1, y: 2, x2: -1, y2: 4}
`))
	if err == nil {
		t.Fatal("want validation errors")
	}
	msg := err.Error()
	for _, want := range []string{"权限名无效", "需要 text", "timeout_ms", "doubletap", "drag"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error misses %q:\n%s", want, msg)
		}
	}
}

type fakeDevice struct {
	serial string
	mu     sync.Mutex
	calls  []string
	reply  func(cmd string) (string, error)
	png    []byte
	pngErr error
}

func (d *fakeDevice) Serial() string { return d.serial }
func (d *fakeDevice) Shell(ctx context.Context, cmd string) (string, error) {
	d.mu.Lock()
	d.calls = append(d.calls, cmd)
	d.mu.Unlock()
	if d.reply != nil {
		return d.reply(cmd)
	}
	return "", nil
}
func (d *fakeDevice) Screenshot(ctx context.Context) ([]byte, error) {
	return d.png, d.pngErr
}

func (d *fakeDevice) allCalls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.calls...)
}

type fakeSampler struct {
	mu      sync.Mutex
	samples int
	err     error
	stopped bool
}

func (s *fakeSampler) Name() string { return "fake" }
func (s *fakeSampler) Start(context.Context) error {
	return nil
}
func (s *fakeSampler) Sample(context.Context) (perf.Sample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.samples++
	return perf.Sample{At: time.Now(), FPS: 60, FrameMS: 16.6, CPU: 20, MemMB: 100, Note: "fake"}, s.err
}
func (s *fakeSampler) Stop() error {
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
	return nil
}

func TestLaunchResolvesAndMeasures(t *testing.T) {
	dev := &fakeDevice{serial: "fake", reply: func(cmd string) (string, error) {
		switch {
		case strings.Contains(cmd, "force-stop"):
			return "", nil
		case strings.Contains(cmd, "resolve-activity"):
			return "priority=0\ncom.example/.Main\n", nil
		case strings.Contains(cmd, "am start -W"):
			return "TotalTime: 1200\nWaitTime: 1300\n", nil
		}
		return "", nil
	}}
	res, err := Launch(context.Background(), dev, "com.example", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Activity != "com.example/.Main" || !res.Measured || res.StartupMS != 1200 {
		t.Errorf("launch = %+v", res)
	}
	calls := strings.Join(dev.allCalls(), "\n")
	for _, want := range []string{"force-stop 'com.example'", "resolve-activity", "am start -W -n 'com.example/.Main'"} {
		if !strings.Contains(calls, want) {
			t.Errorf("calls missing %q:\n%s", want, calls)
		}
	}
}

func TestLaunchFallsBackToMonkeyWithoutFakingStartup(t *testing.T) {
	dev := &fakeDevice{serial: "fake", reply: func(cmd string) (string, error) {
		if strings.Contains(cmd, "resolve-activity") {
			return "No activity found\n", nil
		}
		return "", nil
	}}
	res, err := Launch(context.Background(), dev, "com.example", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Measured || res.StartupMS != 0 {
		t.Errorf("must not fake startup: %+v", res)
	}
	calls := strings.Join(dev.allCalls(), "\n")
	if strings.Contains(calls, "force-stop") {
		t.Error("warm launch must not force-stop")
	}
	if !strings.Contains(calls, "monkey -p 'com.example'") {
		t.Errorf("calls = %s", calls)
	}
}

func runnerPlan() *Plan {
	return &Plan{Version: 1, Defaults: Defaults{Interval: "200ms", Warmup: "0s", Settle: "0s"}, Cases: []Case{{
		Name: "smoke", Package: "com.example", Steps: []Step{
			{Action: "launch", Cold: boolPtr(true)},
			{Action: "tap", X: 10, Y: 20},
			{Action: "wait", MS: 150},
			{Action: "screenshot"},
		},
		Assert: Assert{
			MinFPS: floatPtr(0), MaxJank: floatPtr(0), MaxCPU: floatPtr(0),
			MaxMem: floatPtr(0), MaxTemp: floatPtr(0), MaxFrameP95MS: floatPtr(0),
			MaxStartupMS: floatPtr(0),
		},
	}}}
}

func boolPtr(v bool) *bool { return &v }

func TestRunnerSuccessWritesArtifacts(t *testing.T) {
	root := t.TempDir()
	dev := &fakeDevice{serial: "fake", png: []byte("PNG"), reply: func(cmd string) (string, error) {
		switch {
		case strings.Contains(cmd, "resolve-activity"):
			return "com.example/.Main\n", nil
		case strings.Contains(cmd, "am start -W"):
			return "TotalTime: 900\n", nil
		}
		return "", nil
	}}
	sampler := &fakeSampler{}
	r, err := NewRunner(runnerPlan(), 0, "fake", dev, sampler, root)
	if err != nil {
		t.Fatal(err)
	}
	res := r.Run(context.Background())
	if res.Failed {
		t.Fatalf("res = %+v", res)
	}
	if res.StartupMS != 900 {
		t.Errorf("startup = %v", res.StartupMS)
	}
	if res.Summary.Samples < 1 {
		t.Errorf("samples = %d (immediate sample should be kept)", res.Summary.Samples)
	}
	if res.CSV == "" || res.ResultJSON == "" {
		t.Errorf("artifacts = %+v", res)
	}
	for _, p := range []string{res.CSV, res.ResultJSON, filepath.Join(r.ArtifactsDir(), "step-04.png")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	if !sampler.stopped {
		t.Error("sampler was not stopped")
	}
}

func TestRunnerDeviceFailureCapturesScreenshot(t *testing.T) {
	dev := &fakeDevice{serial: "fake", png: []byte("PNG"), reply: func(cmd string) (string, error) {
		return "", errors.New("offline")
	}}
	r, err := NewRunner(runnerPlan(), 0, "fake", dev, &fakeSampler{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	res := r.Run(context.Background())
	if !res.Failed || !strings.Contains(res.Error, "设备不可用") {
		t.Fatalf("res = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(r.ArtifactsDir(), "failure.png")); err != nil {
		t.Errorf("failure screenshot missing: %v", err)
	}
}

func TestRunnerSamplerErrorIsNotAPass(t *testing.T) {
	dev := &fakeDevice{serial: "fake", png: []byte("PNG")}
	sampler := &fakeSampler{err: errors.New("boom")}
	r, err := NewRunner(runnerPlan(), 0, "fake", dev, sampler, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	res := r.Run(context.Background())
	if !res.Failed || !strings.Contains(res.Error, "没有采到样本") {
		t.Fatalf("res = %+v", res)
	}
}

func TestEvaluateExtraChecks(t *testing.T) {
	s := perf.Summary{FPSMean: 30, FrameSamples: 10, FrameP95: 40, Samples: 10}
	l := DefaultLimits()
	l.Thresholds.MinFPSMean = 48
	l.MaxFrameP95 = 33
	l.MaxStartupMS = 5000
	checks := evaluate(l, s, 6000)
	var fps, p95, startup bool
	for _, c := range checks {
		switch c.Name {
		case "平均帧率":
			fps = !c.OK
		case "帧耗时 P95":
			p95 = !c.OK
		case "冷启动":
			startup = !c.OK
		}
	}
	if !fps || !p95 || !startup {
		t.Errorf("checks = %+v", checks)
	}
	// A zero limit disables the check; missing startup is skipped.
	l.MaxFrameP95 = 0
	l.MaxStartupMS = 0
	for _, c := range evaluate(l, s, 0) {
		if c.Name == "帧耗时 P95" || c.Name == "冷启动" {
			t.Errorf("disabled check still present: %+v", c)
		}
	}
}

func TestSleepCtxAndStepDuration(t *testing.T) {
	if err := sleepCtx(context.Background(), 0); err != nil {
		t.Errorf("zero sleep = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); err == nil {
		t.Error("canceled sleep must fail")
	}
	if got := stepDuration(Step{Seconds: 2}); got != 2*time.Second {
		t.Errorf("seconds = %v", got)
	}
	if got := stepDuration(Step{MS: 300}); got != 300*time.Millisecond {
		t.Errorf("ms = %v", got)
	}
	if got := stepDuration(Step{}); got != 0 {
		t.Errorf("empty = %v", got)
	}
}

func TestRunnerDeepActions(t *testing.T) {
	zero := 0.0
	plan := &Plan{Version: 1, Cases: []Case{{
		Name: "deep", Package: "com.example.game", Steps: []Step{
			{Action: "longpress", X: 10, Y: 20, DurationMS: 900},
			{Action: "doubletap", X: 30, Y: 40},
			{Action: "drag", X: 1, Y: 2, X2: 3, Y2: 4, DurationMS: 700},
			{Action: "stop"},
			{Action: "clear"},
			{Action: "grant", Permissions: []string{"android.permission.CAMERA", "android.permission.RECORD_AUDIO"}},
			{Action: "revoke", Permissions: []string{"android.permission.CAMERA"}},
			{Action: "wait_activity", Text: "com.example.game/.Main", TimeoutMS: 3000},
			{Action: "wait_text", Text: "开始", TimeoutMS: 3000},
			{Action: "assert_activity", Text: ".Main"},
			{Action: "assert_text", Text: "开始"},
			{Action: "assert_not_text", Text: "崩溃"},
			{Action: "assert_package"},
		},
		Assert: Assert{
			MinFPS: &zero, MaxJank: &zero, MaxCPU: &zero, MaxMem: &zero,
			MaxTemp: &zero, MaxFrameP95MS: &zero, MaxStartupMS: &zero,
		},
	}}}
	dev := &fakeDevice{serial: "fake", reply: func(cmd string) (string, error) {
		switch {
		case strings.Contains(cmd, "dumpsys window windows"):
			return "mCurrentFocus=Window{abc u0 com.example.game/.Main}\n", nil
		case strings.Contains(cmd, "uiautomator dump"):
			return "UI hierchary dumped to: /data/local/tmp/qawork-ui.xml\n", nil
		case strings.HasPrefix(cmd, "cat "):
			return `<hierarchy><node text="开始"/></hierarchy>`, nil
		case strings.Contains(cmd, "pm clear"):
			return "Success\n", nil
		case strings.Contains(cmd, "pidof"):
			return "12345\n", nil
		}
		return "", nil
	}}
	r, err := NewRunner(plan, 0, "fake", dev, &fakeSampler{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	res := r.Run(context.Background())
	if res.Failed {
		t.Fatalf("res = %+v", res)
	}
	if len(res.Steps) != len(plan.Cases[0].Steps) {
		t.Errorf("steps = %d", len(res.Steps))
	}
	calls := strings.Join(dev.allCalls(), "\n")
	for _, want := range []string{
		"input swipe 10 20 10 20 900",
		"pm clear com.example.game",
		"pm grant com.example.game android.permission.CAMERA",
		"pm revoke com.example.game android.permission.CAMERA",
		"pidof com.example.game",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("calls missing %q:\n%s", want, calls)
		}
	}
	if strings.Count(calls, "input tap 30 40") != 2 {
		t.Errorf("doubletap calls = %d", strings.Count(calls, "input tap 30 40"))
	}
}

func TestRunnerAssertionFailsLoudly(t *testing.T) {
	zero := 0.0
	plan := &Plan{Version: 1, Cases: []Case{{
		Name: "assert", Package: "com.example.game", Steps: []Step{
			{Action: "assert_text", Text: "不存在的按钮"},
		},
		Assert: Assert{MinFPS: &zero, MaxJank: &zero, MaxCPU: &zero, MaxMem: &zero,
			MaxTemp: &zero, MaxFrameP95MS: &zero, MaxStartupMS: &zero},
	}}}
	dev := &fakeDevice{serial: "fake", png: []byte("PNG"), reply: func(cmd string) (string, error) {
		switch {
		case strings.Contains(cmd, "uiautomator dump"):
			return "UI hierchary dumped\n", nil
		case strings.HasPrefix(cmd, "cat "):
			return `<hierarchy><node text="开始"/></hierarchy>`, nil
		}
		return "", nil
	}}
	r, err := NewRunner(plan, 0, "fake", dev, &fakeSampler{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	res := r.Run(context.Background())
	if !res.Failed || !strings.Contains(res.Error, "界面没有文本") {
		t.Fatalf("res = %+v", res)
	}
}

func TestStepTimeout(t *testing.T) {
	if got := stepTimeout(Step{}); got != 10*time.Second {
		t.Errorf("default = %v", got)
	}
	if got := stepTimeout(Step{TimeoutMS: 2500}); got != 2500*time.Millisecond {
		t.Errorf("timeout = %v", got)
	}
	if got := stepTimeout(Step{TimeoutMS: 999999}); got != 120*time.Second {
		t.Errorf("clamped = %v", got)
	}
}
func TestSafeName(t *testing.T) {
	if got := safeName("登录 主界面!"); got != "" {
		t.Errorf("non-ascii = %q", got)
	}
	if got := safeName("  a/b:c  "); got != "a-b-c" {
		t.Errorf("safe = %q", got)
	}
	if got := safeName(strings.Repeat("x", 60)); len(got) != 40 {
		t.Errorf("len = %d", len(got))
	}
}
