// Package app is the QAWork workbench: the state of one QA session and the
// native MyGo views that draw it.
//
// The default mode is PerfDog-style performance capture: pick a device and
// an app, record frame rate, CPU, memory and temperature at a fixed interval,
// then gate the run against thresholds. Compatibility batches and
// qawork.yaml automation both run real adb work; panels show only what
// actually came back.
package app

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/theme"

	"github.com/neko233-com/QAWork/internal/automation"
	"github.com/neko233-com/QAWork/internal/compat"
	"github.com/neko233-com/QAWork/internal/perf"
	"github.com/neko233-com/QAWork/internal/setup"
	"github.com/neko233-com/QAWork/internal/stress"
	"github.com/neko233-com/QAWork/internal/version"
)

// Host is what the app needs from the window: a way to ask for a rebuild
// after a background sample arrives.
type Host interface {
	Update(fn func())
}

// Bounds of the sample history the UI keeps in memory.
const (
	maxLiveSamples = 600
	chartSamples   = 90
	tableSamples   = 40
)

// The workbench declares four modes. PerfDog performance capture is the
// default; compatibility, automation and stress are real adb workflows, each
// with its own panel. The numbering is part of the app's public shape.
const (
	ModePerfDog = iota
	ModeCompat
	ModeAutomation
	ModeStress
)

// App is the state of the workbench.
type App struct {
	ADB           perf.ADB
	Mode          int // ModePerfDog 默认, ModeCompat, ModeAutomation
	Serial        string
	Target        string // Android package or Windows executable
	Interval      int
	IntervalLabel string

	// ShowSetup opens the environment installer instead of the workbench.
	// It is a separate screen, not a fourth work mode.
	ShowSetup bool

	NominalFPS float64
	Thresholds perf.Thresholds

	Host Host

	mu        sync.Mutex
	samples   []perf.Sample
	running   bool
	status    string
	lastErr   string
	savedPath string
	startedAt time.Time
	collector *perf.Collector
	cancel    context.CancelFunc
	devices   []perf.Device
	deviceErr string
	table     ui.ListState

	setupDeps    []setup.Dep
	setupDir     string
	setupMsg     string
	setupRunning bool

	// Automation panel.
	automationPlanPath string
	automationCase     string
	automationMsg      string
	automationRunning  bool
	automationResults  []automation.Result
	automationTable    ui.ListState

	// Compatibility panel.
	compatPlanPath string
	compatPackage  string
	compatAPK      string
	compatCase     string
	compatMsg      string
	compatRunning  bool
	compatMatrix   compat.Matrix
	compatTable    ui.ListState
	compatRepeat   string
	compatReset    string

	// Stress panel.
	stressPackage  string
	stressDuration string
	stressMsg      string
	stressRunning  bool
	stressResult   stress.Result
}

// New returns the workbench in its default mode: PerfDog-style performance
// capture at one sample per second against a 60 fps target.
func New(host Host) *App {
	return &App{
		ADB:                perf.ADB{},
		Mode:               ModePerfDog,
		Interval:           1,
		IntervalLabel:      "1 s",
		NominalFPS:         60,
		Thresholds:         perf.DefaultThresholds(),
		Host:               host,
		automationPlanPath: "qawork.yaml",
		stressDuration:     "2 分钟",
		compatRepeat:       "1 轮",
		compatReset:        "不清数据",
		status:             "就绪：默认 PerfDog 性能模式",
	}
}

// interval is the sampling period the label selects.
func (a *App) interval() time.Duration {
	switch a.IntervalLabel {
	case "500 ms":
		return 500 * time.Millisecond
	case "2 s":
		return 2 * time.Second
	default:
		return time.Second
	}
}

// DataDir is where captures are saved; QAWORK_HOME overrides it for tests.
func DataDir() string {
	if dir := os.Getenv("QAWORK_HOME"); dir != "" {
		return dir
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, version.Product)
}

// Snapshot copies the samples taken so far.
func (a *App) Snapshot() []perf.Sample {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]perf.Sample, len(a.samples))
	copy(out, a.samples)
	return out
}

// IsRunning reports whether a capture is in flight.
func (a *App) IsRunning() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.running
}

// Status is the line the footer shows.
func (a *App) Status() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lastErr != "" {
		return a.lastErr
	}
	return a.status
}

// SavedPath is the CSV the last finished capture wrote, if any.
func (a *App) SavedPath() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.savedPath
}

// RefreshDevices lists the Android devices adb sees, and always offers the
// PC process target of this machine.
func (a *App) RefreshDevices(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	devices, err := a.ADB.Devices(ctx)
	a.mu.Lock()
	a.devices = append([]perf.Device{{Serial: "local", Model: "本机进程 (PC)", State: "device"}}, devices...)
	a.deviceErr = ""
	if err != nil {
		a.deviceErr = "adb 不可用：" + err.Error()
	}
	if a.Serial == "" && len(a.devices) > 0 {
		a.Serial = a.devices[0].Serial
	}
	a.mu.Unlock()
	if a.Host != nil {
		a.Host.Update(func() {})
	}
}

// SetupState is what the environment screen draws.
func (a *App) SetupState() (deps []setup.Dep, dir, msg string, running bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	deps = append(deps, a.setupDeps...)
	return deps, a.setupDir, a.setupMsg, a.setupRunning
}

// RefreshSetup probes adb, scrcpy and the manual USB driver, and adopts the
// adb QAWork finds so the GUI and CLI use the same binary.
func (a *App) RefreshSetup(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	report := setup.Inspect(ctx)
	a.mu.Lock()
	a.setupDeps = report.Deps
	a.setupDir = report.ToolsDir
	if report.ADBPath != "" && a.ADB.Path == "" {
		a.ADB.Path = report.ADBPath
	}
	a.mu.Unlock()
	if a.Host != nil {
		a.Host.Update(func() {})
	}
}

// InstallDep installs adb or scrcpy in the background: download to the
// tools dir, then append that dir to the current user's PATH. The USB
// driver stays manual on purpose.
func (a *App) InstallDep(id string) {
	a.mu.Lock()
	if a.setupRunning {
		a.mu.Unlock()
		return
	}
	a.setupRunning = true
	a.setupMsg = "准备安装 " + id + "…"
	a.mu.Unlock()

	host := a.Host
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		_, err := setup.Install(ctx, id, true, func(msg string) {
			a.mu.Lock()
			a.setupMsg = msg
			a.mu.Unlock()
			if host != nil {
				host.Update(func() {})
			}
		})
		a.mu.Lock()
		a.setupRunning = false
		if err != nil {
			a.setupMsg = "安装失败：" + err.Error()
		} else {
			a.setupMsg = "安装完成：" + id
		}
		a.mu.Unlock()
		if host != nil {
			host.Update(func() {})
		}
		a.RefreshSetup(context.Background())
	}()
}

// InstallRequired installs every required dependency that is missing.
func (a *App) InstallRequired() {
	deps, _, _, running := a.SetupState()
	if running {
		return
	}
	for _, d := range deps {
		if d.Required && d.Status == setup.StatusMissing && d.Installable {
			a.InstallDep(d.ID)
			return
		}
	}
}

// ValidatePlan checks qawork.yaml without touching a device.
func (a *App) ValidatePlan() {
	path := a.automationPlanPath
	plan, err := automation.LoadPlan(path)
	if err != nil {
		a.mu.Lock()
		a.automationMsg = "读取失败：" + err.Error()
		a.mu.Unlock()
		if a.Host != nil {
			a.Host.Update(func() {})
		}
		return
	}
	errs := plan.Validate()
	msg := fmt.Sprintf("计划有效：%d 个 case", len(plan.Cases))
	if len(errs) > 0 {
		msg = "计划有 " + fmt.Sprint(len(errs)) + " 处问题："
		for _, e := range errs {
			msg += "\n· " + e.Error()
		}
	}
	a.mu.Lock()
	a.automationMsg = msg
	a.mu.Unlock()
	if a.Host != nil {
		a.Host.Update(func() {})
	}
}

// RunPlan loads qawork.yaml and runs the selected cases on the selected
// Android device (or the first online one) in the background.
func (a *App) RunPlan() {
	a.mu.Lock()
	if a.automationRunning {
		a.mu.Unlock()
		return
	}
	if a.running || a.compatRunning || a.stressRunning {
		a.mu.Unlock()
		a.setAutomationMsg("先停止性能采集/兼容矩阵/压测")
		return
	}
	path := a.automationPlanPath
	caseName := a.automationCase
	serial := a.Serial
	if serial == "local" {
		a.mu.Unlock()
		a.setAutomationMsg("自动化只支持 Android；PC 进程没有 adb 回放")
		return
	}
	a.automationRunning = true
	a.automationResults = nil
	a.automationMsg = "加载计划…"
	a.mu.Unlock()

	plan, err := automation.LoadPlan(path)
	if err != nil {
		a.finishAutomation("读取计划失败：" + err.Error())
		return
	}
	if errs := plan.Validate(); len(errs) > 0 {
		a.finishAutomation("计划校验失败：" + errs[0].Error())
		return
	}
	indices, err := automation.SelectIndices(plan, caseName)
	if err != nil {
		a.finishAutomation(err.Error())
		return
	}
	root := filepath.Join(DataDir(), "automation", time.Now().Format("20060102-150405"))
	host := a.Host
	go func() {
		results := automation.Execute(context.Background(), automation.ExecuteOptions{
			ADB: a.ADB, Plan: plan, Indices: indices, Serial: serial,
			ArtifactsRoot: root, Timeout: 10 * time.Minute,
			OnStart: func(_ int, cs automation.Case) {
				a.setAutomationMsg("运行 " + cs.Name + "…")
			},
		})
		a.mu.Lock()
		a.automationRunning = false
		a.automationResults = results
		failed := 0
		for _, r := range results {
			if r.Failed {
				failed++
			}
		}
		if failed == 0 {
			a.automationMsg = fmt.Sprintf("完成：%d 个 case 全部通过", len(results))
		} else {
			a.automationMsg = fmt.Sprintf("完成：%d 个 case，%d 个失败", len(results), failed)
		}
		a.mu.Unlock()
		if host != nil {
			host.Update(func() {})
		}
	}()
}

func (a *App) setAutomationMsg(msg string) {
	a.mu.Lock()
	a.automationMsg = msg
	a.mu.Unlock()
	if a.Host != nil {
		a.Host.Update(func() {})
	}
}

// AutomationState copies the panel state for drawing; background runs write
// these fields, so the view must not read them directly.
func (a *App) AutomationState() (results []automation.Result, msg string, running bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	results = append([]automation.Result(nil), a.automationResults...)
	return results, a.automationMsg, a.automationRunning
}

// AutomationNotice is the footer line for automation mode.
func (a *App) AutomationNotice() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.automationMsg != "" {
		return a.automationMsg
	}
	return "自动化：qawork.yaml 驱动 adb，产物落本地"
}

func (a *App) finishAutomation(msg string) {
	a.mu.Lock()
	a.automationRunning = false
	a.automationMsg = msg
	a.mu.Unlock()
	if a.Host != nil {
		a.Host.Update(func() {})
	}
}

// RunCompat runs every online Android device through the same case set.
func (a *App) RunCompat() {
	a.mu.Lock()
	if a.compatRunning {
		a.mu.Unlock()
		return
	}
	if a.running || a.automationRunning || a.stressRunning {
		a.mu.Unlock()
		a.setCompatMsg("先停止性能采集/自动化/压测")
		return
	}
	planPath := a.compatPlanPath
	pkg := a.compatPackage
	apk := a.compatAPK
	caseName := a.compatCase
	repeat := compatRepeatValue(a.compatRepeat)
	reset := a.compatReset == "每轮清数据"
	a.compatRunning = true
	a.compatMatrix = compat.Matrix{}
	a.compatMsg = "准备兼容矩阵…"
	a.mu.Unlock()

	var plan *automation.Plan
	if planPath != "" {
		p, err := automation.LoadPlan(planPath)
		if err != nil {
			a.finishCompat("读取计划失败：" + err.Error())
			return
		}
		if errs := p.Validate(); len(errs) > 0 {
			a.finishCompat("计划校验失败：" + errs[0].Error())
			return
		}
		plan = p
	}
	if plan == nil && pkg == "" {
		a.finishCompat("没有计划时必须填包名")
		return
	}
	root := filepath.Join(DataDir(), "compat", time.Now().Format("20060102-150405"))
	host := a.Host
	go func() {
		m, err := compat.Run(context.Background(), compat.Options{
			ADB: a.ADB, Plan: plan, Package: pkg, CaseName: caseName, APKPath: apk,
			ArtifactsRoot: root, Timeout: 10 * time.Minute, Repeat: repeat, ResetData: reset,
			OnDevice: func(serial string, done, total int) {
				a.setCompatMsg(fmt.Sprintf("[%d/%d] %s …", done, total, serial))
			},
		})
		a.mu.Lock()
		a.compatRunning = false
		if err != nil {
			a.compatMsg = "兼容矩阵失败：" + err.Error()
		} else {
			a.compatMatrix = m
			failed := 0
			for _, r := range m.Rows {
				if r.Failed {
					failed++
				}
			}
			a.compatMsg = fmt.Sprintf("完成：%d 台 × %d 个用例 × %d 轮，%d 行失败", m.Devices, m.Cases, m.Runs, failed)
		}
		a.mu.Unlock()
		if host != nil {
			host.Update(func() {})
		}
	}()
}

func (a *App) setCompatMsg(msg string) {
	a.mu.Lock()
	a.compatMsg = msg
	a.mu.Unlock()
	if a.Host != nil {
		a.Host.Update(func() {})
	}
}

// CompatState copies the panel state for drawing.
func (a *App) CompatState() (matrix compat.Matrix, msg string, running bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	matrix = a.compatMatrix
	matrix.Rows = append([]compat.Row(nil), a.compatMatrix.Rows...)
	return matrix, a.compatMsg, a.compatRunning
}

// CompatNotice is the footer line for compatibility mode.
func (a *App) CompatNotice() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.compatMsg != "" {
		return a.compatMsg
	}
	return "兼容性：所有在线设备批量跑，失败行带 logcat/截图"
}

func (a *App) finishCompat(msg string) {
	a.mu.Lock()
	a.compatRunning = false
	a.compatMsg = msg
	a.mu.Unlock()
	if a.Host != nil {
		a.Host.Update(func() {})
	}
}

// RunStress launches one real monkey stress run against the selected Android
// device. It never writes a pass when monkey or sampling failed.
func (a *App) RunStress() {
	a.mu.Lock()
	if a.stressRunning {
		a.mu.Unlock()
		return
	}
	if a.running || a.automationRunning || a.compatRunning {
		a.stressMsg = "先停止性能采集/自动化/兼容矩阵"
		a.mu.Unlock()
		return
	}
	pkg := strings.TrimSpace(a.stressPackage)
	if pkg == "" {
		a.stressMsg = "先填要压测的 Android 包名"
		a.mu.Unlock()
		return
	}
	serial := a.Serial
	if serial == "local" {
		serial = ""
	}
	duration := stressDurationValue(a.stressDuration)
	artifacts := filepath.Join(setup.HomeDir(), "stress", time.Now().Format("20060102-150405"))
	a.stressRunning = true
	a.stressResult = stress.Result{Package: pkg, Serial: serial}
	a.stressMsg = "压测中：" + pkg
	a.mu.Unlock()

	go func() {
		res, err := stress.Run(context.Background(), stress.Options{
			ADB: a.ADB, Serial: serial, Package: pkg, Duration: duration,
			Events: 3000, ThrottleMS: 300, Interval: time.Second, ArtifactsRoot: artifacts,
		})
		a.mu.Lock()
		a.stressRunning = false
		if err != nil {
			a.stressResult = stress.Result{Package: pkg, Serial: serial, Failed: true, Error: err.Error()}
			a.stressMsg = "压测失败：" + err.Error()
		} else {
			a.stressResult = res
			if res.Crash {
				a.stressMsg = "完成：检测到 crash"
			} else if res.ANR {
				a.stressMsg = "完成：检测到 ANR"
			} else if res.Failed {
				a.stressMsg = "完成：" + res.Error
			} else {
				a.stressMsg = fmt.Sprintf("完成：%d 个样本，无 crash/ANR", res.Samples)
			}
		}
		a.mu.Unlock()
		if a.Host != nil {
			a.Host.Update(func() {})
		}
	}()
}

// StressState copies the stress panel state for drawing.
func (a *App) StressState() (stress.Result, string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stressResult, a.stressMsg, a.stressRunning
}

// StressNotice is the footer line for stress mode.
func (a *App) StressNotice() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stressMsg != "" {
		return a.stressMsg
	}
	return "压测：monkey 随机输入 + 性能采样，结束后按包名扫 crash/ANR"
}

func compatRepeatValue(label string) int {
	switch label {
	case "2 轮":
		return 2
	case "3 轮":
		return 3
	case "5 轮":
		return 5
	default:
		return 1
	}
}

func stressDurationValue(label string) time.Duration {
	switch label {
	case "1 分钟":
		return time.Minute
	case "10 分钟":
		return 10 * time.Minute
	case "30 分钟":
		return 30 * time.Minute
	default:
		return 2 * time.Minute
	}
}

// Devices returns the last device list.
func (a *App) Devices() ([]perf.Device, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]perf.Device, len(a.devices))
	copy(out, a.devices)
	return out, a.deviceErr
}

// Start begins a capture with the current target and interval.
func (a *App) Start() {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return
	}
	if a.automationRunning || a.compatRunning || a.stressRunning {
		a.lastErr = "自动化/兼容矩阵/压测跑完再采性能"
		a.mu.Unlock()
		return
	}
	if a.Target == "" {
		a.lastErr = "先填目标：Android 包名或 PC 进程名"
		a.mu.Unlock()
		return
	}
	sampler, err := a.sampler()
	if err != nil {
		a.lastErr = err.Error()
		a.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := perf.NewCollector(sampler, a.interval())
	a.collector = c
	a.cancel = cancel
	a.samples = nil
	a.running = true
	a.lastErr = ""
	a.savedPath = ""
	a.startedAt = time.Now()
	a.status = "采集中：" + sampler.Name()
	a.mu.Unlock()

	host := a.Host
	c.OnSample(func(s perf.Sample) {
		a.mu.Lock()
		a.samples = append(a.samples, s)
		if len(a.samples) > maxLiveSamples {
			a.samples = a.samples[len(a.samples)-maxLiveSamples:]
		}
		a.mu.Unlock()
		if host != nil {
			host.Update(func() {})
		}
	})
	go func() {
		c.Run(ctx)
		a.finish(c)
	}()
}

// sampler builds the sampler for the selected target.
func (a *App) sampler() (perf.Sampler, error) {
	if a.Serial == "" {
		return nil, fmt.Errorf("先选设备，或点刷新")
	}
	if a.Serial == "local" {
		return perf.NewLocalSampler(a.Target)
	}
	return perf.NewAndroidSampler(a.ADB, a.Serial, a.Target), nil
}

// finish saves the capture and reports it, once the sampler stopped.
func (a *App) finish(c *perf.Collector) {
	samples := c.Snapshot()
	err := c.Err()

	a.mu.Lock()
	a.running = false
	a.collector = nil
	a.cancel = nil
	a.samples = samples
	if len(samples) > maxLiveSamples {
		a.samples = samples[len(samples)-maxLiveSamples:]
	}
	if err != nil {
		a.lastErr = err.Error()
	}
	path := ""
	if len(samples) > 0 {
		path = filepath.Join(DataDir(), "sessions", sessionName(a.Serial, a.Target))
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr == nil {
			if saveErr := perf.SaveCSV(path, samples); saveErr == nil {
				a.savedPath = path
			} else {
				a.lastErr = "保存 CSV 失败：" + saveErr.Error()
			}
		}
	}
	if a.lastErr == "" {
		a.status = fmt.Sprintf("采集完成：%d 个采样", len(samples))
	}
	if path != "" {
		a.status += "，已存 " + filepath.Base(path)
	}
	a.mu.Unlock()

	if a.Host != nil {
		a.Host.Update(func() {})
	}
}

// Stop ends the capture; finish runs when the sampler returns.
func (a *App) Stop() {
	a.mu.Lock()
	cancel := a.cancel
	if a.running && cancel != nil {
		a.status = "正在停止…"
	}
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func sessionName(serial, target string) string {
	clean := func(s string) string {
		out := make([]rune, 0, len(s))
		for _, r := range s {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
				out = append(out, r)
			default:
				out = append(out, '_')
			}
		}
		return string(out)
	}
	return fmt.Sprintf("%s-%s-%s.csv", clean(serial), clean(target), time.Now().Format("20060102-150405"))
}

// Latest returns the most recent sample, if any.
func (a *App) Latest() (perf.Sample, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.samples) == 0 {
		return perf.Sample{}, false
	}
	return a.samples[len(a.samples)-1], true
}

// Summary aggregates what has been sampled so far, for the live cards.
func (a *App) Summary() perf.Summary {
	return perf.Summarize(a.Snapshot())
}

// View draws one frame of the workbench.
func (a *App) View(c *ui.Context) {
	theme.Apply(c)
	tk := theme.TokensOf(c)

	ui.Column(c).Fill().Background(tk.BgLayout).Children(func() {
		a.header(c, tk)
		ui.Divider(c)
		if a.ShowSetup {
			ui.Column(c).Grow(1).Basis(0).MinHeight(0).Padding(16).Children(func() {
				a.setupPanel(c, tk)
			})
		} else {
			ui.Row(c).Grow(1).Basis(0).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
				a.sidebar(c, tk)
				ui.Column(c).Grow(1).Basis(0).MinHeight(0).Padding(16).Gap(12).Children(func() {
					switch a.Mode {
					case ModeCompat:
						a.compatPanel(c, tk)
					case ModeAutomation:
						a.automationPanel(c, tk)
					case ModeStress:
						a.stressPanel(c, tk)
					default:
						a.perfPanel(c, tk)
					}
				})
			})
		}
		a.footer(c, tk)
	})
}

func (a *App) header(c *ui.Context, tk *theme.Tokens) {
	ui.Row(c).Padding(12, 16).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Text(c, version.Product).FontSize(22).Bold().TextColor(tk.Text)
		if a.ShowSetup {
			ui.Badge(c, "环境安装")
			ui.Spacer(c)
			if ui.Button(c, "返回工作台").Clicked() {
				a.ShowSetup = false
			}
			return
		}
		ui.Badge(c, "对标 PerfDog")
		ui.Segmented(c, &a.Mode, "PerfDog 性能", "兼容性", "自动化", "压测")
		ui.Spacer(c)
		if ui.Button(c, "环境").Clicked() {
			a.ShowSetup = true
			go a.RefreshSetup(context.Background())
		}
		if a.IsRunning() {
			ui.Badge(c, "● 采集中")
		} else {
			ui.Badge(c, "空闲")
		}
	})
}

func (a *App) sidebar(c *ui.Context, tk *theme.Tokens) {
	devices, deviceErr := a.Devices()
	ui.Column(c).Width(300).MinWidth(260).Padding(12).Gap(10).
		Background(tk.BgContainer).Border(1, tk.Border).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "设备").Bold().TextColor(tk.Text)
			ui.Spacer(c)
			if ui.Button(c, "刷新").Clicked() {
				go a.RefreshDevices(context.Background())
			}
		})

		if deviceErr != "" {
			ui.Text(c, deviceErr).FontSize(11).TextColor(tk.Warning).Wrap()
		}
		ui.Scroll(c).MaxHeight(150).Children(func() {
			ui.Column(c).Gap(4).Children(func() {
				if len(devices) == 0 {
					ui.Text(c, "没有设备，插上 USB 或打开 adb。").FontSize(12).TextColor(tk.TextTertiary)
				}
				for _, d := range devices {
					if a.deviceRow(c, tk, d) {
						a.Serial = d.Serial
					}
				}
			})
		})

		ui.Divider(c)
		if a.Mode == ModePerfDog {
			ui.Text(c, "目标包名 / 进程名").FontSize(12).TextColor(tk.TextSecondary)
			ui.TextInput(c, &a.Target).Placeholder("com.example.game 或 Game.exe")

			if last, ok := a.Latest(); ok {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "引擎").FontSize(11).TextColor(tk.TextTertiary)
					if last.Engine == "" {
						ui.Badge(c, "未识别")
					} else {
						ui.Badge(c, last.Engine.Label())
					}
				})
				if last.MainThread != "" {
					ui.Text(c, fmt.Sprintf("%s %.0f%% · %s %.0f%%", last.MainThread, last.MainCPU, last.RenderThread, last.RenderCPU)).
						FontSize(11).TextColor(tk.TextSecondary).Wrap()
				}
			}

			ui.Text(c, "采样间隔").FontSize(12).TextColor(tk.TextSecondary)
			ui.Select(c, &a.IntervalLabel, []string{"500 ms", "1 s", "2 s"})

			if a.IsRunning() {
				if ui.PrimaryButton(c, "停止采集").FillWidth().Clicked() {
					a.Stop()
				}
			} else {
				if ui.PrimaryButton(c, "开始采集").FillWidth().Clicked() {
					a.Start()
				}
			}

			ui.Divider(c)
			a.thresholdLine(c, tk)
		} else if a.Mode == ModeAutomation {
			ui.Text(c, "自动化使用计划里的包名与步骤。这里选中的 Android 设备会覆盖计划 serial；选“本机进程”无效。").
				FontSize(11).TextColor(tk.TextTertiary).Wrap()
		} else if a.Mode == ModeStress {
			ui.Text(c, "压测只跑 Android 真机/模拟器；选中设备留空则取第一台在线。").
				FontSize(11).TextColor(tk.TextTertiary).Wrap()
		} else {
			ui.Text(c, "兼容矩阵默认跑所有在线 Android 设备；侧栏选择不限制批量范围。").
				FontSize(11).TextColor(tk.TextTertiary).Wrap()
		}
	})
}

// deviceRow draws one device and reports whether it was clicked.
func (a *App) deviceRow(c *ui.Context, tk *theme.Tokens, d perf.Device) bool {
	selected := a.Serial == d.Serial
	bg := tk.BgLayout
	if selected {
		bg = tk.FillSecondary
	}
	row := ui.Box(c).FillWidth().Padding(8).Radius(6).Background(bg).Children(func() {
		ui.Column(c).Gap(2).Children(func() {
			ui.Text(c, d.Label()).FontSize(13).TextColor(tk.Text).NoWrap()
			ui.Text(c, d.Serial).FontSize(11).TextColor(tk.TextTertiary).NoWrap()
		})
	})
	return row.Clicked()
}

func (a *App) thresholdLine(c *ui.Context, tk *theme.Tokens) {
	ui.Text(c, fmt.Sprintf("阈值：平均 FPS ≥ %.0f · 卡顿 ≤ %.0f%% · CPU ≤ %.0f%% · 内存 ≤ %.0f MB · 温度 ≤ %.0f°C",
		a.Thresholds.MinFPSMean, a.Thresholds.MaxJankPct, a.Thresholds.MaxCPU, a.Thresholds.MaxMemMB, a.Thresholds.MaxTempC)).
		FontSize(11).TextColor(tk.TextTertiary).Wrap()
}

func (a *App) perfPanel(c *ui.Context, tk *theme.Tokens) {
	samples := a.Snapshot()
	s := perf.Summarize(samples)
	checks := a.Thresholds.Evaluate(s)
	hasFrames := s.FrameSamples > 0
	fpsText, dropText, frameText, jankText := "—", "—", "—", "—"
	hintFPS, hintDrop, hintFrame, hintJank := "无帧率来源", "PC 进程需 Present hook", "无帧率来源", "无帧率来源"
	if hasFrames {
		fpsText = fmt.Sprintf("%.1f", s.FPSMean)
		hintFPS = fmt.Sprintf("最低 %.1f", s.FPSMin)
		dropText = fmt.Sprintf("%.0f%%", perf.DropRate(s.FPSMean, a.NominalFPS))
		hintDrop = fmt.Sprintf("目标 %.0f fps", a.NominalFPS)
		frameText = fmt.Sprintf("%.1f ms", s.FrameP95)
		hintFrame = fmt.Sprintf("P50 %.1f ms", s.FrameP50)
		jankText = fmt.Sprintf("%.0f%%", s.JankPct)
		hintJank = fmt.Sprintf("上限 %.0f%%", a.Thresholds.MaxJankPct)
	}

	ui.Row(c).Gap(10).AlignItems(ui.Stretch).Children(func() {
		a.statCard(c, tk, "平均 FPS", fpsText, hintFPS, tk.Primary)
		a.statCard(c, tk, "掉帧率", dropText, hintDrop, tk.Warning)
		a.statCard(c, tk, "帧耗时 P95", frameText, hintFrame, tk.Info)
		a.statCard(c, tk, "卡顿帧", jankText, hintJank, tk.Warning)
		cpuHint := fmt.Sprintf("均值 %.0f%%", s.CPUMean)
		if s.MainThread != "" {
			cpuHint = fmt.Sprintf("主 %.0f%% / 渲 %.0f%%", s.MainCPUMax, s.RenderCPUMax)
		}
		a.statCard(c, tk, "CPU 峰值", fmt.Sprintf("%.0f%%", s.CPUMax), cpuHint, tk.Primary)
		a.statCard(c, tk, "内存峰值", fmt.Sprintf("%.0f MB", s.MemMaxMB), fmt.Sprintf("温度峰值 %.0f°C", s.TempMax), tk.Primary)
	})

	ui.Row(c).Height(150).Gap(12).Children(func() {
		var fpsValues []float64
		if hasFrames {
			fpsValues = fpsSeries(samples, chartSamples)
		}
		a.chartCard(c, tk, "帧率", fpsValues, a.NominalFPS, tk.Primary)
		a.chartCard(c, tk, "CPU %", cpuSeries(samples, chartSamples), 100, tk.Success)
	})

	a.verdict(c, tk, checks, len(samples) > 0, hasFrames)

	ui.Column(c).Grow(1).Basis(0).MinHeight(0).Gap(6).Children(func() {
		ui.Text(c, "采样记录").FontSize(12).TextColor(tk.TextSecondary)
		if len(samples) == 0 {
			ui.Text(c, "还没有采样。选设备、填目标，点开始采集。").
				FontSize(12).TextColor(tk.TextTertiary).Wrap()
			return
		}
		ui.Scroll(c).Grow(1).Children(func() {
			a.sampleTable(c, tk, samples)
		})
	})
}

func (a *App) statCard(c *ui.Context, tk *theme.Tokens, label, value, hint string, tint ui.Color) {
	ui.Column(c).Grow(1).Basis(0).Padding(12).Gap(4).
		Background(tk.BgContainer).Radius(tk.BorderRadius).Border(1, tk.Border).Children(func() {
		ui.Text(c, label).FontSize(11).TextColor(tk.TextTertiary).NoWrap()
		ui.Text(c, value).FontSize(22).Bold().TextColor(tint).NoWrap()
		ui.Text(c, hint).FontSize(11).TextColor(tk.TextSecondary).NoWrap()
	})
}

func (a *App) chartCard(c *ui.Context, tk *theme.Tokens, title string, values []float64, maxV float64, color ui.Color) {
	ui.Column(c).Grow(1).Basis(0).Height(150).Padding(12).Gap(6).
		Background(tk.BgContainer).Radius(tk.BorderRadius).Border(1, tk.Border).Children(func() {
		ui.Text(c, title).FontSize(11).TextColor(tk.TextTertiary).NoWrap()
		ui.Spacer(c)
		if len(values) == 0 {
			ui.Text(c, "等待采样…").FontSize(11).TextColor(tk.TextTertiary)
			return
		}
		ui.Row(c).Height(92).Gap(1.5).AlignItems(ui.End).Children(func() {
			for _, v := range values {
				ui.Box(c).Grow(1).Basis(0).Height(barHeight(v, maxV)).Radius(2).Background(color)
			}
		})
	})
}

func (a *App) verdict(c *ui.Context, tk *theme.Tokens, checks []perf.Check, sampled, hasFrames bool) {
	if len(checks) == 0 {
		return
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "门禁").FontSize(12).TextColor(tk.TextSecondary)
		switch {
		case !sampled:
			ui.Badge(c, "待采样")
		case perf.Failed(checks):
			ui.Badge(c, "未通过")
		case !hasFrames:
			ui.Badge(c, "无帧率数据")
		default:
			ui.Badge(c, "通过")
		}
		for _, ch := range checks {
			color := tk.Success
			if !ch.OK {
				color = tk.Error
			}
			ui.Text(c, fmt.Sprintf("%s %.1f/%.1f %s", ch.Name, ch.Value, ch.Limit, ch.Unit)).
				FontSize(11).TextColor(color).NoWrap()
		}
	})
}

func (a *App) sampleTable(c *ui.Context, tk *theme.Tokens, samples []perf.Sample) {
	columns := []ui.TableColumn{
		{Title: "时间", Width: 92, Fixed: true},
		{Title: "FPS", Width: 62},
		{Title: "帧耗时", Width: 76},
		{Title: "卡顿", Width: 62},
		{Title: "CPU", Width: 62},
		{Title: "内存", Width: 80},
		{Title: "温度", Width: 62},
		{Title: "主/渲", Width: 88},
		{Title: "来源", Width: 160},
	}
	rows := make([]perf.Sample, 0, tableSamples)
	for i := len(samples) - 1; i >= 0 && len(rows) < tableSamples; i-- {
		rows = append(rows, samples[i])
	}
	cells := []func(perf.Sample) string{
		func(s perf.Sample) string { return s.At.Format("15:04:05") },
		func(s perf.Sample) string { return fmt.Sprintf("%.1f", s.FPS) },
		func(s perf.Sample) string { return fmt.Sprintf("%.1f ms", s.FrameMS) },
		func(s perf.Sample) string { return fmt.Sprintf("%.1f%%", s.JankPct) },
		func(s perf.Sample) string { return fmt.Sprintf("%.0f%%", s.CPU) },
		func(s perf.Sample) string { return fmt.Sprintf("%.0f MB", s.MemMB) },
		func(s perf.Sample) string { return fmt.Sprintf("%.0f°C", s.TempC) },
		func(s perf.Sample) string {
			if s.MainThread == "" && s.RenderThread == "" {
				return "—"
			}
			return fmt.Sprintf("%.0f/%.0f%%", s.MainCPU, s.RenderCPU)
		},
		func(s perf.Sample) string { return s.Note },
	}
	ui.Table(c, &a.table, columns, len(rows), func(row, col int) {
		if row < 0 || row >= len(rows) || col < 0 || col >= len(cells) {
			return
		}
		ui.Text(c, cells[col](rows[row])).FontSize(12).TextColor(tk.Text).NoWrap()
	})
}

// setupPanel is the environment installer: each dependency can be installed
// on its own, adb is marked required, and the USB driver is explicitly
// manual rather than a fake one-click button.
func (a *App) setupPanel(c *ui.Context, tk *theme.Tokens) {
	deps, dir, msg, running := a.SetupState()
	missingRequired := false
	for _, d := range deps {
		if d.Required && d.Status == setup.StatusMissing {
			missingRequired = true
		}
	}

	ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
		ui.Text(c, "环境安装").FontSize(18).Bold().TextColor(tk.Text)
		ui.Badge(c, "独立工具")
		ui.Spacer(c)
		if ui.Button(c, "重新检测").Clicked() && !running {
			go a.RefreshSetup(context.Background())
		}
	})
	usbText := "USB 驱动由厂商提供，QAWork 不代装。"
	if runtime.GOOS == "darwin" {
		usbText = "macOS USB 免驱，不需要厂商驱动。"
	}
	ui.Text(c, "只装你需要的。adb 必装；scrcpy 可选；"+usbText).
		FontSize(12).TextColor(tk.TextSecondary).Wrap()
	if dir != "" {
		ui.Text(c, "安装目录： "+dir+"（只写当前用户 PATH，不需要管理员权限）").
			FontSize(11).TextColor(tk.TextTertiary).Wrap()
	}

	ui.Scroll(c).Grow(1).Basis(0).MinHeight(0).Children(func() {
		ui.Column(c).Gap(8).Children(func() {
			if len(deps) == 0 {
				ui.Text(c, "正在检测 adb / scrcpy…").FontSize(12).TextColor(tk.TextTertiary)
			}
			for _, d := range deps {
				a.setupRow(c, tk, d, running)
			}
		})
	})

	ui.Divider(c)
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		if missingRequired && !running {
			if ui.PrimaryButton(c, "一键安装必装").Clicked() {
				a.InstallRequired()
			}
		}
		ui.Text(c, msg).FontSize(11).TextColor(tk.TextSecondary).Wrap()
	})
}

func (a *App) setupRow(c *ui.Context, tk *theme.Tokens, d setup.Dep, running bool) {
	ui.Column(c).FillWidth().Padding(12).Gap(5).
		Background(tk.BgContainer).Radius(tk.BorderRadius).Border(1, tk.Border).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, d.Name).FontSize(14).Bold().TextColor(tk.Text)
			if d.Required {
				ui.Badge(c, "必装")
			} else {
				ui.Badge(c, "可选")
			}
			ui.Spacer(c)
			switch d.Status {
			case setup.StatusOK:
				ui.Badge(c, "已安装")
			case setup.StatusManual:
				ui.Badge(c, "需手动")
			default:
				ui.Badge(c, "未安装")
			}
			if d.Installable && d.Status != setup.StatusOK && !running {
				if ui.Button(c, "安装").Clicked() {
					a.InstallDep(d.ID)
				}
			}
		})
		ui.Text(c, d.Description).FontSize(11).TextColor(tk.TextSecondary).Wrap()
		if d.Version != "" {
			ui.Text(c, d.Version).FontSize(11).TextColor(tk.TextTertiary).NoWrap()
		}
		if d.Path != "" && d.Status == setup.StatusOK {
			ui.Text(c, d.Path).FontSize(11).TextColor(tk.TextTertiary).NoWrap()
		}
		if d.Note != "" {
			ui.Text(c, d.Note).FontSize(11).TextColor(tk.TextTertiary).Wrap()
		}
	})
}

func (a *App) compatPanel(c *ui.Context, tk *theme.Tokens) {
	matrix, msg, running := a.CompatState()
	ui.Column(c).Gap(10).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "兼容性矩阵").FontSize(16).Bold().TextColor(tk.Text)
			ui.Badge(c, "可用")
		})
		ui.Text(c, "所有在线 Android 设备 × 用例 × 重复轮次：批量安装 APK、冷启动、短采样、抓 crash/ANR，结果落 CSV/JSON/HTML/JUnit。").
			FontSize(12).TextColor(tk.TextSecondary).Wrap()

		ui.Row(c).Gap(8).AlignItems(ui.End).Children(func() {
			ui.Column(c).Grow(1).Basis(0).Gap(3).Children(func() {
				ui.Text(c, "计划（可选）").FontSize(11).TextColor(tk.TextTertiary)
				ui.TextInput(c, &a.compatPlanPath).Placeholder("qawork.yaml；留空只做启动 smoke")
			})
			ui.Column(c).Grow(1).Basis(0).Gap(3).Children(func() {
				ui.Text(c, "包名（无计划时必填）").FontSize(11).TextColor(tk.TextTertiary)
				ui.TextInput(c, &a.compatPackage).Placeholder("com.example.game")
			})
		})
		ui.Row(c).Gap(8).AlignItems(ui.End).Children(func() {
			ui.Column(c).Grow(1).Basis(0).Gap(3).Children(func() {
				ui.Text(c, "APK（可选，先批量安装）").FontSize(11).TextColor(tk.TextTertiary)
				ui.TextInput(c, &a.compatAPK).Placeholder("build/app.apk")
			})
			ui.Column(c).Width(160).Gap(3).Children(func() {
				ui.Text(c, "只跑指定 case").FontSize(11).TextColor(tk.TextTertiary)
				ui.TextInput(c, &a.compatCase).Placeholder("留空跑全部")
			})
			ui.Column(c).Width(100).Gap(3).Children(func() {
				ui.Text(c, "重复轮次").FontSize(11).TextColor(tk.TextTertiary)
				ui.Select(c, &a.compatRepeat, []string{"1 轮", "2 轮", "3 轮", "5 轮"})
			})
			ui.Column(c).Width(130).Gap(3).Children(func() {
				ui.Text(c, "应用数据").FontSize(11).TextColor(tk.TextTertiary)
				ui.Select(c, &a.compatReset, []string{"不清数据", "每轮清数据"})
			})
			if running {
				ui.Badge(c, "运行中")
			} else if ui.PrimaryButton(c, "运行兼容矩阵").Clicked() {
				a.RunCompat()
			}
		})
		if msg != "" {
			ui.Text(c, msg).FontSize(11).TextColor(tk.TextSecondary).Wrap()
		}

		if len(matrix.Rows) == 0 {
			ui.Divider(c)
			for _, item := range []string{
				"多机型批量安装、冷启动、首帧耗时",
				"型号 / Android / SDK / ABI / 分辨率矩阵",
				"崩溃与 ANR 按包名扫描，避免别的 App 污染结果",
				"矩阵 CSV/JSON + HTML 报告 + JUnit 与每设备截图、logcat 一起落盘",
			} {
				ui.Text(c, "· "+item).FontSize(12).TextColor(tk.TextSecondary).Wrap()
			}
			return
		}

		ui.Text(c, fmt.Sprintf("%d 台设备 × %d 个用例 × %d 轮，%.1fs%s", matrix.Devices, matrix.Cases, matrix.Runs, matrix.DurationSeconds, passText(matrix.Failed))).
			FontSize(12).TextColor(tk.TextSecondary).Wrap()
		ui.Scroll(c).Grow(1).Basis(0).MinHeight(0).Children(func() {
			a.compatResultTable(c, tk, matrix.Rows)
		})
		if matrix.CSV != "" {
			ui.Text(c, "矩阵 CSV："+matrix.CSV).FontSize(11).TextColor(tk.TextTertiary).NoWrap()
		}
		if matrix.JSON != "" {
			ui.Text(c, "矩阵 JSON："+matrix.JSON).FontSize(11).TextColor(tk.TextTertiary).NoWrap()
		}
		if matrix.HTML != "" {
			ui.Text(c, "HTML 报告："+matrix.HTML).FontSize(11).TextColor(tk.TextTertiary).NoWrap()
		}
		if matrix.JUnit != "" {
			ui.Text(c, "JUnit："+matrix.JUnit).FontSize(11).TextColor(tk.TextTertiary).NoWrap()
		}
	})
}

func (a *App) compatResultTable(c *ui.Context, tk *theme.Tokens, rows []compat.Row) {
	columns := []ui.TableColumn{
		{Title: "设备", Width: 130, Fixed: true},
		{Title: "型号", Width: 110},
		{Title: "Android", Width: 70},
		{Title: "用例", Width: 110},
		{Title: "安装", Width: 54},
		{Title: "启动", Width: 70},
		{Title: "FPS", Width: 54},
		{Title: "Crash", Width: 54},
		{Title: "ANR", Width: 46},
		{Title: "结果", Width: 70},
	}
	ui.Table(c, &a.compatTable, columns, len(rows), func(row, col int) {
		if row < 0 || row >= len(rows) {
			return
		}
		r := rows[row]
		var text string
		switch col {
		case 0:
			text = r.Serial
		case 1:
			text = r.Model
		case 2:
			text = r.Android
		case 3:
			text = r.Case
		case 4:
			if r.InstallSkipped {
				text = "跳过"
			} else {
				text = yesNo(r.Installed)
			}
		case 5:
			text = msText(r.StartupMS)
		case 6:
			text = fpsText(r.FPSMean)
		case 7:
			text = yesNo(r.Crash)
		case 8:
			text = yesNo(r.ANR)
		case 9:
			if r.Failed {
				text = "失败"
			} else {
				text = "通过"
			}
		}
		color := tk.Text
		if r.Failed {
			color = tk.Error
		}
		ui.Text(c, text).FontSize(12).TextColor(color).NoWrap()
	})
}

func (a *App) stressPanel(c *ui.Context, tk *theme.Tokens) {
	res, msg, running := a.StressState()
	ui.Column(c).Gap(10).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "稳定性压测").FontSize(16).Bold().TextColor(tk.Text)
			ui.Badge(c, "可用")
		})
		ui.Text(c, "monkey 长随机输入 + 全程性能采样，结束后按包名扫 logcat 的 crash/ANR，证据落 samples.csv 与 result.json。").
			FontSize(12).TextColor(tk.TextSecondary).Wrap()
		ui.Row(c).Gap(8).AlignItems(ui.End).Children(func() {
			ui.Column(c).Grow(1).Basis(0).Gap(3).Children(func() {
				ui.Text(c, "Android 包名").FontSize(11).TextColor(tk.TextTertiary)
				ui.TextInput(c, &a.stressPackage).Placeholder("com.example.game")
			})
			ui.Column(c).Width(130).Gap(3).Children(func() {
				ui.Text(c, "时长").FontSize(11).TextColor(tk.TextTertiary)
				ui.Select(c, &a.stressDuration, []string{"1 分钟", "2 分钟", "10 分钟", "30 分钟"})
			})
			if running {
				ui.Badge(c, "压测中")
			} else if ui.PrimaryButton(c, "开始压测").Clicked() {
				a.RunStress()
			}
		})
		if msg != "" {
			ui.Text(c, msg).FontSize(11).TextColor(tk.TextSecondary).Wrap()
		}
		if res.Package == "" {
			ui.Divider(c)
			for _, item := range []string{
				"固定 3000 个 monkey 事件，默认 300ms 间隔，可被时长截断",
				"边跑边采 FPS / frame P95 / CPU / 内存 / 温度",
				"crash/ANR 按包名匹配，避免别的 App 污染",
				"产物：samples.csv、result.json、logcat.txt、monkey.txt",
			} {
				ui.Text(c, "· "+item).FontSize(12).TextColor(tk.TextSecondary).Wrap()
			}
			return
		}
		ui.Divider(c)
		ui.Row(c).Gap(18).AlignItems(ui.Center).Children(func() {
			ui.Text(c, fmt.Sprintf("样本 %d", res.Samples)).FontSize(12).TextColor(tk.TextSecondary)
			ui.Text(c, "FPS "+fpsText(res.Summary.FPSMean)).FontSize(12).TextColor(tk.TextSecondary)
			ui.Text(c, "P95 "+msText(res.Summary.FrameP95)).FontSize(12).TextColor(tk.TextSecondary)
			ui.Text(c, "CPU "+pctText(res.Summary.CPUMax)).FontSize(12).TextColor(tk.TextSecondary)
			ui.Text(c, "内存 "+mbText(res.Summary.MemMaxMB)).FontSize(12).TextColor(tk.TextSecondary)
			ui.Text(c, "Crash "+yesNo(res.Crash)).FontSize(12).TextColor(tk.TextSecondary)
			ui.Text(c, "ANR "+yesNo(res.ANR)).FontSize(12).TextColor(tk.TextSecondary)
		})
		if res.Error != "" {
			ui.Text(c, "错误："+res.Error).FontSize(12).TextColor(tk.Error).Wrap()
		}
		for _, p := range []string{res.CSV, res.ResultJSON, res.Logcat, res.MonkeyFile} {
			if p != "" {
				ui.Text(c, p).FontSize(11).TextColor(tk.TextTertiary).NoWrap()
			}
		}
	})
}

func (a *App) automationPanel(c *ui.Context, tk *theme.Tokens) {
	results, msg, running := a.AutomationState()
	ui.Column(c).Gap(10).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "自动化用例").FontSize(16).Bold().TextColor(tk.Text)
			ui.Badge(c, "可用")
		})
		ui.Text(c, "qawork.yaml 驱动 adb：launch / tap / 长按 / 双击 / swipe / drag / 按键 / 文本 / 权限 / 等待与断言 / 截图，全程采样并盖性能门禁。").
			FontSize(12).TextColor(tk.TextSecondary).Wrap()

		ui.Row(c).Gap(8).AlignItems(ui.End).Children(func() {
			ui.Column(c).Grow(1).Basis(0).Gap(3).Children(func() {
				ui.Text(c, "计划文件").FontSize(11).TextColor(tk.TextTertiary)
				ui.TextInput(c, &a.automationPlanPath).Placeholder("qawork.yaml")
			})
			ui.Column(c).Width(180).Gap(3).Children(func() {
				ui.Text(c, "只跑指定 case").FontSize(11).TextColor(tk.TextTertiary)
				ui.TextInput(c, &a.automationCase).Placeholder("留空跑全部")
			})
			if ui.Button(c, "校验").Clicked() && !running {
				a.ValidatePlan()
			}
			if running {
				ui.Badge(c, "运行中")
			} else if ui.PrimaryButton(c, "运行计划").Clicked() {
				a.RunPlan()
			}
		})
		if msg != "" {
			ui.Text(c, msg).FontSize(11).TextColor(tk.TextSecondary).Wrap()
		}

		if len(results) == 0 {
			ui.Divider(c)
			ui.Text(c, "示例：").
				FontSize(12).TextColor(tk.TextTertiary)
			for _, line := range []string{
				"cases:",
				"  - name: 登录到主界面",
				"    package: com.example.game",
				"    steps:",
				"      - {action: launch, cold: true}",
				"      - {action: wait_activity, activity: .Main}",
				"      - {action: assert_package}",
				"      - {action: tap, x: 540, y: 1600}",
			} {
				ui.Text(c, "  "+line).FontSize(12).TextColor(tk.TextSecondary).NoWrap()
			}
			return
		}
		ui.Scroll(c).Grow(1).Basis(0).MinHeight(0).Children(func() {
			a.automationResultTable(c, tk, results)
		})
		if dir := automationArtifactsDir(results); dir != "" {
			ui.Text(c, "产物："+dir).FontSize(11).TextColor(tk.TextTertiary).NoWrap()
		}
	})
}

func (a *App) automationResultTable(c *ui.Context, tk *theme.Tokens, results []automation.Result) {
	columns := []ui.TableColumn{
		{Title: "用例", Width: 150, Fixed: true},
		{Title: "包名", Width: 180},
		{Title: "结果", Width: 70},
		{Title: "FPS", Width: 60},
		{Title: "启动", Width: 80},
		{Title: "失败检查", Width: 90},
		{Title: "说明", Width: 220},
	}
	ui.Table(c, &a.automationTable, columns, len(results), func(row, col int) {
		if row < 0 || row >= len(results) {
			return
		}
		r := results[row]
		var text string
		switch col {
		case 0:
			text = r.Case
		case 1:
			text = r.Package
		case 2:
			if r.Failed {
				text = "失败"
			} else {
				text = "通过"
			}
		case 3:
			text = fmt.Sprintf("%.1f", r.Summary.FPSMean)
		case 4:
			text = msText(r.StartupMS)
		case 5:
			text = fmt.Sprintf("%d", failedCheckCount(r.Checks))
		case 6:
			if r.Error != "" {
				text = r.Error
			} else {
				text = failedCheckNames(r.Checks)
			}
		}
		color := tk.Text
		if r.Failed {
			color = tk.Error
		}
		ui.Text(c, text).FontSize(12).TextColor(color).NoWrap()
	})
}

func yesNo(v bool) string {
	if v {
		return "是"
	}
	return "否"
}

func msText(v float64) string {
	if v <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.0fms", v)
}

func fpsText(v float64) string {
	if v <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f", v)
}

func pctText(v float64) string {
	if v <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", v)
}

func mbText(v float64) string {
	if v <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.0fMB", v)
}

func passText(failed bool) string {
	if failed {
		return "，有失败"
	}
	return "，全部通过"
}

func failedCheckCount(checks []perf.Check) int {
	n := 0
	for _, c := range checks {
		if !c.OK {
			n++
		}
	}
	return n
}

func failedCheckNames(checks []perf.Check) string {
	var out []string
	for _, c := range checks {
		if !c.OK {
			out = append(out, c.Name)
		}
	}
	if len(out) == 0 {
		return "—"
	}
	s := out[0]
	for _, n := range out[1:] {
		s += "、" + n
	}
	return s
}

func automationArtifactsDir(results []automation.Result) string {
	for _, r := range results {
		for _, p := range r.Artifacts {
			if st, err := os.Stat(p); err == nil {
				if st.IsDir() {
					return p
				}
				return filepath.Dir(p)
			}
		}
	}
	return ""
}

func (a *App) footer(c *ui.Context, tk *theme.Tokens) {
	status := a.Status()
	if a.ShowSetup {
		status = "环境安装：只写当前用户目录与 PATH，不需要管理员权限"
	} else if a.Mode == ModeAutomation {
		status = a.AutomationNotice()
	} else if a.Mode == ModeCompat {
		status = a.CompatNotice()
	} else if a.Mode == ModeStress {
		status = a.StressNotice()
	}
	ui.Row(c).Padding(8, 16).Gap(12).AlignItems(ui.Center).Background(tk.BgContainer).Children(func() {
		ui.Text(c, status).FontSize(11).TextColor(tk.TextSecondary).NoWrap()
		ui.Spacer(c)
		if path := a.SavedPath(); path != "" {
			ui.Text(c, filepath.Base(path)).FontSize(11).TextColor(tk.TextTertiary).NoWrap()
		}
		ui.Text(c, "v"+version.Version).FontSize(11).TextColor(tk.TextTertiary).NoWrap()
	})
}

func fpsSeries(samples []perf.Sample, n int) []float64 {
	values := make([]float64, 0, n)
	for i := len(samples) - n; i < len(samples); i++ {
		if i >= 0 {
			values = append(values, samples[i].FPS)
		}
	}
	return values
}

func cpuSeries(samples []perf.Sample, n int) []float64 {
	values := make([]float64, 0, n)
	for i := len(samples) - n; i < len(samples); i++ {
		if i >= 0 {
			values = append(values, samples[i].CPU)
		}
	}
	return values
}

func barHeight(v, maxV float64) float32 {
	if maxV <= 0 {
		return 2
	}
	return float32(2 + math.Min(v, maxV)/maxV*86)
}
