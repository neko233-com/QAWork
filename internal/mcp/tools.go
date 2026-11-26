package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/neko233-com/QAWork/internal/perf"
	"github.com/neko233-com/QAWork/internal/setup"
)

func defaultTools() []Tool {
	return []Tool{
		{
			Name: "qawork_devices",
			Description: "列出 adb 看到的 Android 设备（含本机进程 local 之外的 USB/模拟器）。" +
				"Agent 排查前先跑这个，别猜序列号。",
			Schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
			Run: toolDevices,
		},
		{
			Name: "qawork_perf",
			Description: "对 Android 包或本机进程采一段性能：帧率、帧耗时、卡顿、CPU、内存、温度，" +
				"Unity/UE5 还会带主线程与渲染线程 CPU。返回汇总、逐样本、门禁结果；可同时落 CSV。" +
				"默认 Android 优先；serial=local 走 PC 进程，PC 没有帧率来源。",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"package":          map[string]any{"type": "string", "description": "Android 包名，如 com.example.game；local 时是进程名"},
					"serial":           map[string]any{"type": "string", "description": "设备序列号；留空自动取第一台在线设备；local 为本机进程"},
					"duration_seconds": map[string]any{"type": "number", "description": "采集时长，默认 10，上限 120"},
					"interval_ms":      map[string]any{"type": "number", "description": "采样间隔毫秒，默认 1000，最小 200"},
					"csv":              map[string]any{"type": "string", "description": "可选：CSV 输出路径"},
					"thresholds":       thresholdSchema(),
				},
				"required": []string{"package"},
			},
			Run: toolPerf,
		},
		{
			Name:        "qawork_report",
			Description: "复判一份 QAWork CSV：汇总 + 门禁。Agent 拿到旧结果或 CI 产物时用，不必重采。",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"csv":        map[string]any{"type": "string", "description": "CSV 路径"},
					"thresholds": thresholdSchema(),
				},
				"required": []string{"csv"},
			},
			Run: toolReport,
		},
		{
			Name: "qawork_doctor",
			Description: "检查本机环境：adb 是否可用、scrcpy 是否安装、USB 驱动是否需手动装、工具装在哪个目录。" +
				"采不到设备先跑这个。",
			Schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
			Run: toolDoctor,
		},
	}
}

func thresholdSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"min_fps":    map[string]any{"type": "number", "description": "平均帧率下限，默认 48"},
			"max_jank":   map[string]any{"type": "number", "description": "卡顿帧占比上限 %，默认 5"},
			"max_cpu":    map[string]any{"type": "number", "description": "CPU 峰值上限 %，默认 70"},
			"max_mem":    map[string]any{"type": "number", "description": "内存峰值上限 MB，默认 1024"},
			"max_temp_c": map[string]any{"type": "number", "description": "温度峰值上限 °C，默认 45"},
		},
	}
}

type perfResult struct {
	Target  string        `json:"target"`
	Summary perf.Summary  `json:"summary"`
	Checks  []perf.Check  `json:"checks"`
	Failed  bool          `json:"failed"`
	Samples []perf.Sample `json:"samples"`
	CSV     string        `json:"csv,omitempty"`
}

func toolDevices(ctx context.Context, args map[string]any) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	adb := perf.ADB{Path: setup.ADBPath()}
	devices, err := adb.Devices(ctx)
	if err != nil {
		return nil, fmt.Errorf("adb 不可用：%w（可跑 qawork_doctor 检查环境）", err)
	}
	return map[string]any{
		"count":   len(devices),
		"devices": devices,
		"note":    "local 表示本机进程；PC 进程没有帧率来源",
	}, nil
}

func toolPerf(ctx context.Context, args map[string]any) (any, error) {
	pkg := argString(args, "package")
	if pkg == "" {
		return nil, fmt.Errorf("缺少 package")
	}
	serial := argString(args, "serial")
	if serial == "" {
		serial = firstSerial(ctx)
		if serial == "" {
			return nil, fmt.Errorf("没有在线 Android 设备；先 qawork_devices 或 qawork_doctor")
		}
	}
	duration := argFloat(args, "duration_seconds", 10)
	if duration <= 0 {
		duration = 10
	}
	if duration > 120 {
		duration = 120
	}
	interval := time.Duration(argFloat(args, "interval_ms", 1000)) * time.Millisecond
	if interval < 200*time.Millisecond {
		interval = 200 * time.Millisecond
	}
	tk := thresholdsOf(args)
	adb := perf.ADB{Path: setup.ADBPath()}

	var sampler perf.Sampler
	if serial == "local" {
		s, err := perf.NewLocalSampler(pkg)
		if err != nil {
			return nil, err
		}
		sampler = s
	} else {
		sampler = perf.NewAndroidSampler(adb, serial, pkg)
	}

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(duration*float64(time.Second)))
	defer cancel()
	samples, err := perf.RunCapture(runCtx, sampler, interval, nil)
	if err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		return nil, fmt.Errorf("没有采到样本：设备在线？包名在跑？")
	}
	csvPath := argString(args, "csv")
	if csvPath != "" {
		if err := perf.SaveCSV(csvPath, samples); err != nil {
			return nil, fmt.Errorf("写 CSV: %w", err)
		}
	}
	summary := perf.Summarize(samples)
	checks := tk.Evaluate(summary)
	return perfResult{
		Target:  sampler.Name(),
		Summary: summary,
		Checks:  checks,
		Failed:  perf.Failed(checks),
		Samples: samples,
		CSV:     csvPath,
	}, nil
}

func toolReport(ctx context.Context, args map[string]any) (any, error) {
	csvPath := argString(args, "csv")
	if csvPath == "" {
		return nil, fmt.Errorf("缺少 csv")
	}
	samples, err := perf.LoadCSV(csvPath)
	if err != nil {
		return nil, fmt.Errorf("读 CSV: %w", err)
	}
	summary := perf.Summarize(samples)
	checks := thresholdsOf(args).Evaluate(summary)
	return map[string]any{
		"csv":     csvPath,
		"summary": summary,
		"checks":  checks,
		"failed":  perf.Failed(checks),
	}, nil
}

func toolDoctor(ctx context.Context, args map[string]any) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	report := setup.Inspect(ctx)
	usable := report.ADBPath != ""
	return map[string]any{
		"environment": report,
		"adb_usable":  usable,
		"next":        nextStep(report.Deps),
	}, nil
}

func nextStep(deps []setup.Dep) string {
	for _, d := range deps {
		if d.Required && d.Status == setup.StatusMissing {
			return "必装 adb：GUI 点“环境”或 `qawork setup install -id adb`"
		}
	}
	return "环境就绪：qawork_devices 选设备，再 qawork_perf"
}

func firstSerial(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	devices, err := (perf.ADB{Path: setup.ADBPath()}).Devices(ctx)
	if err != nil {
		return ""
	}
	for _, d := range devices {
		if d.State == "device" {
			return d.Serial
		}
	}
	return ""
}

func thresholdsOf(args map[string]any) perf.Thresholds {
	tk := perf.DefaultThresholds()
	m, _ := args["thresholds"].(map[string]any)
	if m == nil {
		return tk
	}
	tk.MinFPSMean = argFloat(m, "min_fps", tk.MinFPSMean)
	tk.MaxJankPct = argFloat(m, "max_jank", tk.MaxJankPct)
	tk.MaxCPU = argFloat(m, "max_cpu", tk.MaxCPU)
	tk.MaxMemMB = argFloat(m, "max_mem", tk.MaxMemMB)
	tk.MaxTempC = argFloat(m, "max_temp_c", argFloat(m, "max_tempC", tk.MaxTempC))
	return tk
}

func argString(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func argFloat(args map[string]any, key string, def float64) float64 {
	if v, ok := args[key].(float64); ok {
		return v
	}
	return def
}
