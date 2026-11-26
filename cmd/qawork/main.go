// Command qawork is QAWork's headless side: list devices, capture a run,
// and gate a CSV in CI. It shares the samplers and the CSV format with the
// GUI, so a capture from either matches.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"text/tabwriter"
	"time"

	"github.com/neko233-com/QAWork/internal/perf"
	"github.com/neko233-com/QAWork/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "version", "-v", "--version":
		fmt.Println(version.Product, version.Version)
	case "devices", "dev":
		os.Exit(cmdDevices(os.Args[2:]))
	case "perf":
		os.Exit(cmdPerf(os.Args[2:]))
	case "report":
		os.Exit(cmdReport(os.Args[2:]))
	case "setup":
		os.Exit(cmdSetup(os.Args[2:]))
	case "mcp":
		os.Exit(cmdMCP())
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "未知命令 %q\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `QAWork %s — 游戏 QA 工作台

用法:
  qawork devices [-adb PATH] [-json]        列出 adb 设备
  qawork perf -package PKG [-serial S] ...  采集一次并写 CSV，失败退出码 2
  qawork report -csv FILE                   汇总 CSV 并按阈值判定
  qawork setup [check|install] [-id adb]    检查/安装 adb、scrcpy（只写用户 PATH）
  qawork mcp                                stdio MCP 服务，给 agent 直接调

perf 常用参数:
  -serial 设备序列号（local 采集本机进程）  -package Android 包名或 PC 进程名
  -interval 1s  -duration 30s  -csv out.csv  -json
  -min-fps 48  -max-jank 5  -max-cpu 70  -max-mem 1024  -max-temp 45
`, version.Version)
}

func thresholds(fs *flag.FlagSet) *perf.Thresholds {
	tk := perf.DefaultThresholds()
	fs.Float64Var(&tk.MinFPSMean, "min-fps", tk.MinFPSMean, "平均帧率下限")
	fs.Float64Var(&tk.MaxJankPct, "max-jank", tk.MaxJankPct, "卡顿帧占比上限 %")
	fs.Float64Var(&tk.MaxCPU, "max-cpu", tk.MaxCPU, "CPU 峰值上限 %")
	fs.Float64Var(&tk.MaxMemMB, "max-mem", tk.MaxMemMB, "内存峰值上限 MB")
	fs.Float64Var(&tk.MaxTempC, "max-temp", tk.MaxTempC, "温度峰值上限 °C")
	return &tk
}

func cmdDevices(args []string) int {
	fs := flag.NewFlagSet("devices", flag.ExitOnError)
	adbPath := fs.String("adb", "", "adb 可执行文件，默认取 PATH")
	asJSON := fs.Bool("json", false, "输出 JSON")
	fs.Parse(args)

	adb := perf.ADB{Path: *adbPath}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	devices, err := adb.Devices(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取设备失败:", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(devices)
		return 0
	}
	if len(devices) == 0 {
		fmt.Println("没有设备。插上 USB 并打开调试，或起一个模拟器。")
		return 0
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SERIAL\tSTATE\tMODEL")
	for _, d := range devices {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", d.Serial, d.State, d.Model)
	}
	tw.Flush()
	return 0
}

func cmdPerf(args []string) int {
	fs := flag.NewFlagSet("perf", flag.ExitOnError)
	adbPath := fs.String("adb", "", "adb 可执行文件，默认取 PATH")
	serial := fs.String("serial", "", "设备序列号；local 表示本机进程")
	pkg := fs.String("package", "", "Android 包名或 PC 进程名")
	interval := fs.Duration("interval", time.Second, "采样间隔")
	duration := fs.Duration("duration", 30*time.Second, "采集时长")
	csvPath := fs.String("csv", "", "输出 CSV 路径")
	nominal := fs.Float64("nominal", 60, "目标帧率，用于掉帧率")
	asJSON := fs.Bool("json", false, "汇总以 JSON 输出")
	tk := thresholds(fs)
	fs.Parse(args)

	if *pkg == "" {
		fmt.Fprintln(os.Stderr, "缺少 -package")
		return 1
	}
	adb := perf.ADB{Path: *adbPath}
	var sampler perf.Sampler
	if *serial == "local" {
		s, err := perf.NewLocalSampler(*pkg)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		sampler = s
	} else {
		sampler = perf.NewAndroidSampler(adb, *serial, *pkg)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *duration)
	defer cancel()

	started := time.Now()
	samples, err := perf.RunCapture(ctx, sampler, *interval, func(s perf.Sample) {
		line := fmt.Sprintf("%s fps=%.1f cpu=%.0f%% mem=%.0fMB temp=%.1f°C",
			s.At.Format("15:04:05"), s.FPS, s.CPU, s.MemMB, s.TempC)
		if s.MainThread != "" {
			line += fmt.Sprintf(" %s=%.0f%% %s=%.0f%%", s.MainThread, s.MainCPU, s.RenderThread, s.RenderCPU)
		}
		if s.Engine != "" {
			line += " engine=" + s.Engine.Label()
		}
		fmt.Fprintln(os.Stderr, line)
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "采集失败:", err)
		return 1
	}
	if len(samples) == 0 {
		fmt.Fprintln(os.Stderr, "没有采到样本")
		return 1
	}

	if *csvPath != "" {
		if err := perf.SaveCSV(*csvPath, samples); err != nil {
			fmt.Fprintln(os.Stderr, "写 CSV 失败:", err)
			return 1
		}
	}

	summary := perf.Summarize(samples)
	checks := tk.Evaluate(summary)
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(struct {
			Target  string       `json:"target"`
			Elapsed float64      `json:"elapsed_seconds"`
			Summary perf.Summary `json:"summary"`
			Checks  []perf.Check `json:"checks"`
			CSV     string       `json:"csv,omitempty"`
			Failed  bool         `json:"failed"`
		}{
			Target:  sampler.Name(),
			Elapsed: time.Since(started).Seconds(),
			Summary: summary, Checks: checks, CSV: *csvPath, Failed: perf.Failed(checks),
		})
	} else {
		printSummary(os.Stdout, sampler.Name(), summary, checks, *nominal)
	}
	if perf.Failed(checks) {
		return 2
	}
	return 0
}

func cmdReport(args []string) int {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	csvPath := fs.String("csv", "", "要汇总的 CSV")
	asJSON := fs.Bool("json", false, "汇总以 JSON 输出")
	tk := thresholds(fs)
	fs.Parse(args)

	if *csvPath == "" {
		fmt.Fprintln(os.Stderr, "缺少 -csv")
		return 1
	}
	samples, err := perf.LoadCSV(*csvPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读 CSV 失败:", err)
		return 1
	}
	summary := perf.Summarize(samples)
	checks := tk.Evaluate(summary)
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(struct {
			Summary perf.Summary `json:"summary"`
			Checks  []perf.Check `json:"checks"`
			Failed  bool         `json:"failed"`
		}{summary, checks, perf.Failed(checks)})
	} else {
		printSummary(os.Stdout, *csvPath, summary, checks, 60)
	}
	if perf.Failed(checks) {
		return 2
	}
	return 0
}

func printSummary(w io.Writer, target string, s perf.Summary, checks []perf.Check, nominal float64) {
	fmt.Fprintf(w, "目标: %s\n", target)
	if s.EngineLabel != "" {
		if s.MainThread != "" {
			fmt.Fprintf(w, "引擎: %s | %s 峰值 %.0f%% %s 峰值 %.0f%%\n", s.EngineLabel, s.MainThread, s.MainCPUMax, s.RenderThread, s.RenderCPUMax)
		} else {
			fmt.Fprintf(w, "引擎: %s\n", s.EngineLabel)
		}
	}
	fmt.Fprintf(w, "采样: %d 个 / %.1f s\n", s.Samples, s.DurationSeconds)
	fmt.Fprintf(w, "帧率: 均值 %.1f 最低 %.1f | 掉帧 %.0f%%\n", s.FPSMean, s.FPSMin, perf.DropRate(s.FPSMean, nominal))
	fmt.Fprintf(w, "帧耗时: P50 %.1f ms  P95 %.1f ms  P99 %.1f ms\n", s.FrameP50, s.FrameP95, s.FrameP99)
	fmt.Fprintf(w, "卡顿帧 %.1f%% | CPU 峰值 %.0f%% 均值 %.0f%% | 内存峰值 %.0f MB | 温度峰值 %.1f°C\n",
		s.JankPct, s.CPUMax, s.CPUMean, s.MemMaxMB, s.TempMax)
	if len(checks) == 0 {
		return
	}
	fmt.Fprintln(w, "门禁:")
	for _, c := range checks {
		state := "通过"
		if !c.OK {
			state = "未通过"
		}
		fmt.Fprintf(w, "  [%s] %s %.1f（限 %.1f %s）\n", state, c.Name, c.Value, c.Limit, c.Unit)
	}
}
