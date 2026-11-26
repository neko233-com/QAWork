package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/neko233-com/QAWork/internal/adbkit"
	"github.com/neko233-com/QAWork/internal/automation"
	"github.com/neko233-com/QAWork/internal/compat"
	"github.com/neko233-com/QAWork/internal/perf"
	"github.com/neko233-com/QAWork/internal/setup"
	"github.com/neko233-com/QAWork/internal/stress"
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
		{
			Name:        "qawork_validate_plan",
			Description: "校验 qawork.yaml 自动化计划：case 名、步骤、阈值、时长。不碰设备，改完计划先跑这个。",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"plan": map[string]any{"type": "string", "description": "qawork.yaml 路径"},
				},
				"required": []string{"plan"},
			},
			Run: toolValidatePlan,
		},
		{
			Name: "qawork_run_plan",
			Description: "按 qawork.yaml 自动跑 Android 用例：launch/tap/swipe/wait 步骤 + 全程性能采样 + 门禁。" +
				"返回每个 case 的摘要、步骤、检查与产物路径（CSV/JSON/失败截图）。",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"plan":            map[string]any{"type": "string", "description": "qawork.yaml 路径"},
					"case":            map[string]any{"type": "string", "description": "只跑指定 case；留空跑全部"},
					"serial":          map[string]any{"type": "string", "description": "覆盖计划里的设备序列号"},
					"artifacts":       map[string]any{"type": "string", "description": "产物根目录，留空用 QAWORK_HOME/automation"},
					"timeout_seconds": map[string]any{"type": "number", "description": "单个 case 超时，默认 600，上限 1800"},
				},
				"required": []string{"plan"},
			},
			Run: toolRunPlan,
		},
		{
			Name: "qawork_compat_matrix",
			Description: "批量兼容测试：所有在线设备（或指定 devices）× 用例，安装 APK、冷启动、短采样、抓 crash/ANR，" +
				"输出矩阵 CSV/JSON。没有 plan 时只做启动 smoke。",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"plan":            map[string]any{"type": "string", "description": "可选 qawork.yaml"},
					"case":            map[string]any{"type": "string", "description": "只跑指定 case"},
					"package":         map[string]any{"type": "string", "description": "无 plan 时的包名"},
					"apk":             map[string]any{"type": "string", "description": "可选 APK 路径，先批量安装"},
					"devices":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "设备序列号；留空全部在线"},
					"artifacts":       map[string]any{"type": "string", "description": "产物根目录"},
					"timeout_seconds": map[string]any{"type": "number", "description": "单个 case 超时，默认 600，上限 1800"},
					"repeat":          map[string]any{"type": "number", "description": "每台设备每个 case 重复轮数，默认 1，上限 100"},
					"reset":           map[string]any{"type": "boolean", "description": "每轮前 pm clear 应用数据"},
				},
			},
			Run: toolCompatMatrix,
		},
		{
			Name: "qawork_device_info",
			Description: "读取一台设备的型号、品牌、Android/SDK、ABI、分辨率、密度、电量/电池温度。" +
				"serial 留空取第一台在线设备。",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"serial": map[string]any{"type": "string", "description": "设备序列号；留空第一台在线"},
				},
			},
			Run: toolDeviceInfo,
		},
		{
			Name:        "qawork_logcat",
			Description: "读取设备 logcat 最后 N 行，可选先清空、可选写到宿主文件。排查 crash/ANR 用。",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"serial": map[string]any{"type": "string", "description": "设备序列号；留空第一台在线"},
					"lines":  map[string]any{"type": "number", "description": "最后 N 行，默认 500，上限 5000"},
					"clear":  map[string]any{"type": "boolean", "description": "先 logcat -c"},
					"output": map[string]any{"type": "string", "description": "可选宿主输出文件"},
				},
			},
			Run: toolLogcat,
		},
		{
			Name: "qawork_app",
			Description: "设备上的显式应用操作：install / uninstall / start / stop / clear / grant / revoke / animations。" +
				"不提供任意 shell 透传；start 返回解析到的 activity 与启动耗时。",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action":      map[string]any{"type": "string", "enum": []string{"install", "uninstall", "start", "stop", "clear", "grant", "revoke", "animations"}, "description": "操作"},
					"serial":      map[string]any{"type": "string", "description": "设备序列号；留空第一台在线"},
					"package":     map[string]any{"type": "string", "description": "Android 包名"},
					"apk":         map[string]any{"type": "string", "description": "install 时的 APK 路径"},
					"activity":    map[string]any{"type": "string", "description": "start 时显式 activity；留空自动解析"},
					"cold":        map[string]any{"type": "boolean", "description": "start 是否冷启动，默认 false"},
					"permissions": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "grant/revoke 的权限列表"},
					"keep":        map[string]any{"type": "boolean", "description": "uninstall 保留数据"},
					"animations":  map[string]any{"type": "string", "enum": []string{"on", "off"}, "description": "animations 动作的开关"},
				},
				"required": []string{"action"},
			},
			Run: toolApp,
		},
		{
			Name: "qawork_stress",
			Description: "长随机 monkey 压测：先启动应用，再边跑随机输入边采样，结束后按包名扫 crash/ANR，" +
				"输出 CSV/JSON/logcat/monkey 证据。可传 devices 批量跑。",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"package":          map[string]any{"type": "string", "description": "Android 包名"},
					"serial":           map[string]any{"type": "string", "description": "单台设备；留空第一台在线"},
					"devices":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "多台设备序列号"},
					"duration_seconds": map[string]any{"type": "number", "description": "最长时长，默认 300，上限 7200"},
					"events":           map[string]any{"type": "number", "description": "monkey 事件数，默认 3000，上限 1000000"},
					"throttle_ms":      map[string]any{"type": "number", "description": "事件间隔毫秒，默认 300"},
					"interval_ms":      map[string]any{"type": "number", "description": "采样间隔毫秒，默认 1000"},
					"artifacts":        map[string]any{"type": "string", "description": "产物根目录"},
				},
				"required": []string{"package"},
			},
			Run: toolStress,
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

func toolValidatePlan(ctx context.Context, args map[string]any) (any, error) {
	path := argString(args, "plan")
	if path == "" {
		return nil, fmt.Errorf("缺少 plan")
	}
	plan, err := automation.LoadPlan(path)
	if err != nil {
		if verr, ok := err.(*automation.ValidationError); ok {
			return map[string]any{"valid": false, "errors": errorStrings(verr.Errors)}, nil
		}
		return nil, err
	}
	return map[string]any{
		"valid": true, "version": plan.Version, "cases": plan.Names(), "path": path,
	}, nil
}

func toolRunPlan(ctx context.Context, args map[string]any) (any, error) {
	path := argString(args, "plan")
	if path == "" {
		return nil, fmt.Errorf("缺少 plan")
	}
	plan, err := automation.LoadPlan(path)
	if err != nil {
		return nil, err
	}
	indices, err := automation.SelectIndices(plan, argString(args, "case"))
	if err != nil {
		return nil, err
	}
	timeout := argFloat(args, "timeout_seconds", 600)
	if timeout <= 0 {
		timeout = 600
	}
	if timeout > 1800 {
		timeout = 1800
	}
	root := argString(args, "artifacts")
	if root == "" {
		root = filepath.Join(setup.HomeDir(), "automation", time.Now().Format("20060102-150405"))
	}
	adb := perf.ADB{Path: setup.ADBPath()}
	results := automation.Execute(ctx, automation.ExecuteOptions{
		ADB: adb, Plan: plan, Indices: indices, Serial: argString(args, "serial"),
		ArtifactsRoot: root, Timeout: time.Duration(timeout) * time.Second,
	})
	anyFail := false
	for _, r := range results {
		if r.Failed {
			anyFail = true
		}
	}
	return map[string]any{
		"plan": path, "results": results, "failed": anyFail, "artifacts": root,
	}, nil
}

func errorStrings(errs []error) []string {
	out := make([]string, 0, len(errs))
	for _, err := range errs {
		out = append(out, err.Error())
	}
	return out
}

func toolCompatMatrix(ctx context.Context, args map[string]any) (any, error) {
	var plan *automation.Plan
	if path := argString(args, "plan"); path != "" {
		p, err := automation.LoadPlan(path)
		if err != nil {
			return nil, err
		}
		plan = p
	}
	timeout := argFloat(args, "timeout_seconds", 600)
	if timeout <= 0 {
		timeout = 600
	}
	if timeout > 1800 {
		timeout = 1800
	}
	root := argString(args, "artifacts")
	if root == "" {
		root = filepath.Join(setup.HomeDir(), "compat", time.Now().Format("20060102-150405"))
	}
	matrix, err := compat.Run(ctx, compat.Options{
		ADB: perf.ADB{Path: setup.ADBPath()}, Plan: plan, CaseName: argString(args, "case"),
		Package: argString(args, "package"), APKPath: argString(args, "apk"),
		Devices: argStringSlice(args, "devices"), ArtifactsRoot: root,
		Timeout: time.Duration(timeout) * time.Second,
		Repeat:  int(argFloat(args, "repeat", 1)), ResetData: argBool(args, "reset"),
	})
	if err != nil {
		return nil, err
	}
	return matrix, nil
}

func toolDeviceInfo(ctx context.Context, args map[string]any) (any, error) {
	serial, err := serialOrFirst(ctx, argString(args, "serial"))
	if err != nil {
		return nil, err
	}
	info, err := (adbkit.Client{ADB: perf.ADB{Path: setup.ADBPath()}}).Info(ctx, serial)
	if err != nil {
		return nil, err
	}
	return info, nil
}

func toolLogcat(ctx context.Context, args map[string]any) (any, error) {
	serial, err := serialOrFirst(ctx, argString(args, "serial"))
	if err != nil {
		return nil, err
	}
	lines := int(argFloat(args, "lines", 500))
	if lines <= 0 {
		lines = 500
	}
	if lines > 5000 {
		lines = 5000
	}
	client := adbkit.Client{ADB: perf.ADB{Path: setup.ADBPath()}}
	logText, err := client.LogcatDump(ctx, serial, lines, argBool(args, "clear"))
	if err != nil {
		return nil, err
	}
	out := argString(args, "output")
	if out != "" {
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(out, []byte(logText), 0o644); err != nil {
			return nil, err
		}
		return map[string]any{"serial": serial, "path": out, "lines": lines}, nil
	}
	return map[string]any{"serial": serial, "log": logText, "lines": lines}, nil
}

func toolApp(ctx context.Context, args map[string]any) (any, error) {
	action := argString(args, "action")
	serial, err := serialOrFirst(ctx, argString(args, "serial"))
	if err != nil {
		return nil, err
	}
	client := adbkit.Client{ADB: perf.ADB{Path: setup.ADBPath()}}
	pkg := argString(args, "package")
	switch action {
	case "install":
		apk := argString(args, "apk")
		out, err := client.Install(ctx, serial, apk, true, true)
		if err != nil {
			return nil, err
		}
		if !strings.Contains(strings.ToLower(out), "success") {
			return nil, fmt.Errorf("安装未返回 Success: %s", strings.TrimSpace(out))
		}
		return map[string]any{"serial": serial, "apk": apk, "output": out, "success": true}, nil
	case "uninstall":
		out, err := client.Uninstall(ctx, serial, pkg, argBool(args, "keep"))
		if err != nil {
			return nil, err
		}
		if !strings.Contains(strings.ToLower(out), "success") {
			return nil, fmt.Errorf("卸载未返回 Success: %s", strings.TrimSpace(out))
		}
		return map[string]any{"serial": serial, "package": pkg, "output": out}, nil
	case "start":
		dev := automation.NewADBDevice(perf.ADB{Path: setup.ADBPath()}, serial)
		return automation.Launch(ctx, dev, pkg, argString(args, "activity"), argBool(args, "cold"))
	case "stop":
		return simpleClientResult(client.Stop(ctx, serial, pkg))
	case "clear":
		out, err := client.Clear(ctx, serial, pkg)
		if err != nil {
			return nil, err
		}
		if !strings.Contains(strings.ToLower(out), "success") {
			return nil, fmt.Errorf("pm clear 未返回 Success: %s", strings.TrimSpace(out))
		}
		return map[string]any{"serial": serial, "package": pkg, "output": out, "success": true}, nil
	case "grant":
		out, err := client.Grant(ctx, serial, pkg, argStringSlice(args, "permissions"))
		return map[string]any{"serial": serial, "package": pkg, "permissions": out}, err
	case "revoke":
		out, err := client.Revoke(ctx, serial, pkg, argStringSlice(args, "permissions"))
		return map[string]any{"serial": serial, "package": pkg, "permissions": out}, err
	case "animations":
		state := argString(args, "animations")
		if state != "on" && state != "off" {
			return nil, fmt.Errorf("animations 需要 on 或 off")
		}
		if err := client.Animations(ctx, serial, state == "on"); err != nil {
			return nil, err
		}
		return map[string]any{"serial": serial, "animations": state}, nil
	default:
		return nil, fmt.Errorf("未知 action %q", action)
	}
}

func simpleClientResult(out string, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return map[string]any{"output": out}, nil
}

func toolStress(ctx context.Context, args map[string]any) (any, error) {
	pkg := argString(args, "package")
	if pkg == "" {
		return nil, fmt.Errorf("缺少 package")
	}
	duration := argFloat(args, "duration_seconds", 300)
	if duration <= 0 {
		duration = 300
	}
	if duration > 7200 {
		duration = 7200
	}
	events := int(argFloat(args, "events", 3000))
	throttle := int(argFloat(args, "throttle_ms", 300))
	interval := time.Duration(argFloat(args, "interval_ms", 1000)) * time.Millisecond
	root := argString(args, "artifacts")
	if root == "" {
		root = filepath.Join(setup.HomeDir(), "stress", time.Now().Format("20060102-150405"))
	}
	serials := argStringSlice(args, "devices")
	if len(serials) == 0 {
		serials = []string{argString(args, "serial")}
	}
	var results []stress.Result
	for _, serial := range serials {
		oneRoot := root
		if serial != "" {
			oneRoot = filepath.Join(root, nameSafe(serial))
		}
		res, err := stress.Run(ctx, stress.Options{
			ADB: perf.ADB{Path: setup.ADBPath()}, Serial: serial, Package: pkg,
			Duration: time.Duration(duration * float64(time.Second)), Events: events,
			ThrottleMS: throttle, Interval: interval, ArtifactsRoot: oneRoot,
		})
		if err != nil {
			return nil, err
		}
		results = append(results, res)
	}
	failed := false
	for _, r := range results {
		if r.Failed {
			failed = true
		}
	}
	return map[string]any{"package": pkg, "results": results, "failed": failed, "artifacts": root}, nil
}

func serialOrFirst(ctx context.Context, requested string) (string, error) {
	if strings.TrimSpace(requested) != "" {
		return strings.TrimSpace(requested), nil
	}
	serial := firstSerial(ctx)
	if serial == "" {
		return "", fmt.Errorf("没有在线 Android 设备；先 qawork_devices")
	}
	return serial, nil
}

func nameSafe(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-.")
}

func argStringSlice(args map[string]any, key string) []string {
	raw, ok := args[key]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case string:
		var out []string
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
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

func argBool(args map[string]any, key string) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return false
}
