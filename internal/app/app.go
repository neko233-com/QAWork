// Package app is the QAWork workbench: the state of one QA session and the
// native MyGo views that draw it.
//
// The default mode is PerfDog-style performance capture: pick a device and
// an app, record frame rate, CPU, memory and temperature at a fixed interval,
// then gate the run against thresholds. Compatibility and automation are
// declared modes with honest "not wired yet" panels, not hidden mock data.
package app

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/theme"

	"github.com/neko233-com/QAWork/internal/perf"
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

// The workbench declares three modes. Only PerfDog performance capture is
// wired; compatibility and automation are labelled as planned, never
// filled with mock data. The default stays PerfDog, and the numbering is
// part of the app's public shape.
const (
	ModePerfDog = iota
	ModeCompat
	ModeAutomation
)

// App is the state of the workbench.
type App struct {
	ADB           perf.ADB
	Mode          int // ModePerfDog 默认, ModeCompat, ModeAutomation
	Serial        string
	Target        string // Android package or Windows executable
	Interval      int
	IntervalLabel string

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
}

// New returns the workbench in its default mode: PerfDog-style performance
// capture at one sample per second against a 60 fps target.
func New(host Host) *App {
	return &App{
		ADB:           perf.ADB{},
		Mode:          ModePerfDog,
		Interval:      1,
		IntervalLabel: "1 s",
		NominalFPS:    60,
		Thresholds:    perf.DefaultThresholds(),
		Host:          host,
		status:        "就绪：默认 PerfDog 性能模式",
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
		ui.Row(c).Grow(1).Basis(0).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			a.sidebar(c, tk)
			ui.Column(c).Grow(1).Basis(0).MinHeight(0).Padding(16).Gap(12).Children(func() {
				switch a.Mode {
				case ModeCompat:
					a.compatPanel(c, tk)
				case ModeAutomation:
					a.automationPanel(c, tk)
				default:
					a.perfPanel(c, tk)
				}
			})
		})
		a.footer(c, tk)
	})
}

func (a *App) header(c *ui.Context, tk *theme.Tokens) {
	ui.Row(c).Padding(12, 16).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Text(c, version.Product).FontSize(22).Bold().TextColor(tk.Text)
		ui.Badge(c, "对标 PerfDog")
		ui.Segmented(c, &a.Mode, "PerfDog 性能", "兼容性", "自动化")
		ui.Spacer(c)
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
		ui.Text(c, "目标包名 / 进程名").FontSize(12).TextColor(tk.TextSecondary)
		ui.TextInput(c, &a.Target).Placeholder("com.example.game 或 Game.exe")

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
		a.statCard(c, tk, "CPU 峰值", fmt.Sprintf("%.0f%%", s.CPUMax), fmt.Sprintf("均值 %.0f%%", s.CPUMean), tk.Primary)
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
		{Title: "线程", Width: 58},
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
		func(s perf.Sample) string { return fmt.Sprintf("%d", s.Threads) },
		func(s perf.Sample) string { return s.Note },
	}
	ui.Table(c, &a.table, columns, len(rows), func(row, col int) {
		if row < 0 || row >= len(rows) || col < 0 || col >= len(cells) {
			return
		}
		ui.Text(c, cells[col](rows[row])).FontSize(12).TextColor(tk.Text).NoWrap()
	})
}

func (a *App) compatPanel(c *ui.Context, tk *theme.Tokens) {
	ui.Column(c).Gap(10).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "兼容性矩阵").FontSize(16).Bold().TextColor(tk.Text)
			ui.Badge(c, "规划中")
		})
		ui.Text(c, "PerfDog 只做性能；兼容性在 QAWork 里是另一条线：本地设备农场，不上传安装包。").
			FontSize(12).TextColor(tk.TextSecondary).Wrap()
		for _, item := range []string{
			"多机型批量安装、冷启动、首帧耗时",
			"分辨率 / 系统版本 / ABI 矩阵，结果本地落盘",
			"崩溃与 ANR 率统计，同一 CSV 口径进 CI",
			"预留云真机通道：租云设备或自建设备池，二选一",
		} {
			ui.Text(c, "· "+item).FontSize(12).TextColor(tk.TextSecondary).Wrap()
		}
		ui.Divider(c)
		ui.Text(c, "先把 PerfDog 模式的口径做准，再接矩阵——否则兼容性报告没有可比性。").
			FontSize(12).TextColor(tk.TextTertiary).Wrap()
	})
}

func (a *App) automationPanel(c *ui.Context, tk *theme.Tokens) {
	ui.Column(c).Gap(10).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "自动化用例").FontSize(16).Bold().TextColor(tk.Text)
			ui.Badge(c, "规划中")
		})
		ui.Text(c, "用例写进 qawork.yaml，由 adb 回放，跑完直接盖性能门禁。").
			FontSize(12).TextColor(tk.TextSecondary).Wrap()
		ui.Text(c, "yaml 目标形态：").FontSize(12).TextColor(tk.TextTertiary)
		for _, line := range []string{
			"case: 登录到主界面",
			"steps: [launch, tap 540 1600, wait 8s, swipe 540 1600 540 600]",
			"perf: {interval: 1s, min_fps: 48, max_jank: 5}",
		} {
			ui.Text(c, "  "+line).FontSize(12).TextColor(tk.TextSecondary).NoWrap()
		}
	})
}

func (a *App) footer(c *ui.Context, tk *theme.Tokens) {
	ui.Row(c).Padding(8, 16).Gap(12).AlignItems(ui.Center).Background(tk.BgContainer).Children(func() {
		ui.Text(c, a.Status()).FontSize(11).TextColor(tk.TextSecondary).NoWrap()
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
