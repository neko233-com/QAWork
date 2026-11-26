// Package compat runs the same scenario across every connected Android
// device and produces one matrix: device, OS, install, startup, perf,
// crash/ANR, and pass/fail. It is the batch compatibility mode behind
// `qawork compat`, the GUI panel, and the MCP tool.
package compat

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/neko233-com/QAWork/internal/automation"
	"github.com/neko233-com/QAWork/internal/perf"
)

// Options configures one compatibility batch.
type Options struct {
	ADB           perf.ADB
	Plan          *automation.Plan // optional; nil means a launch smoke case
	CaseName      string
	Package       string   // required when Plan is nil
	APKPath       string   // optional; installs before running
	Devices       []string // empty means all online devices
	ArtifactsRoot string
	Timeout       time.Duration // per case
	OnDevice      func(serial string, done, total int)
}

// DeviceInfo is the static row header of the matrix.
type DeviceInfo struct {
	Serial     string `json:"serial"`
	Model      string `json:"model,omitempty"`
	Android    string `json:"android,omitempty"`
	SDK        string `json:"sdk,omitempty"`
	ABI        string `json:"abi,omitempty"`
	Resolution string `json:"resolution,omitempty"`
}

// Row is one device × case result.
type Row struct {
	DeviceInfo
	Case           string       `json:"case"`
	Package        string       `json:"package"`
	Installed      bool         `json:"installed"`
	InstallSkipped bool         `json:"install_skipped,omitempty"`
	InstallLog     string       `json:"install_log,omitempty"`
	StartupMS      float64      `json:"startup_ms,omitempty"`
	FPSMean        float64      `json:"fps_mean,omitempty"`
	FPSMin         float64      `json:"fps_min,omitempty"`
	FrameP95       float64      `json:"frame_p95_ms,omitempty"`
	JankPct        float64      `json:"jank_pct,omitempty"`
	CPUMax         float64      `json:"cpu_max,omitempty"`
	MemMaxMB       float64      `json:"mem_max_mb,omitempty"`
	TempMax        float64      `json:"temp_max,omitempty"`
	Checks         []perf.Check `json:"checks,omitempty"`
	FailedChecks   int          `json:"failed_checks"`
	Crash          bool         `json:"crash"`
	ANR            bool         `json:"anr"`
	Failed         bool         `json:"failed"`
	Error          string       `json:"error,omitempty"`
	CSV            string       `json:"csv,omitempty"`
	ResultJSON     string       `json:"result_json,omitempty"`
	Logcat         string       `json:"logcat,omitempty"`
	Screenshot     string       `json:"screenshot,omitempty"`
	Artifacts      []string     `json:"artifacts,omitempty"`
}

// Matrix is the whole batch, with evidence paths.
type Matrix struct {
	StartedAt       time.Time `json:"started_at"`
	DurationSeconds float64   `json:"duration_seconds"`
	Devices         int       `json:"devices"`
	Cases           int       `json:"cases"`
	Rows            []Row     `json:"rows"`
	Failed          bool      `json:"failed"`
	CSV             string    `json:"csv,omitempty"`
	JSON            string    `json:"json,omitempty"`
	Root            string    `json:"root,omitempty"`
}

// Run executes the compatibility matrix. It returns a Matrix even when a
// device fails; a fatal setup error (no devices, bad plan) is returned as
// err.
func Run(ctx context.Context, opts Options) (Matrix, error) {
	m := Matrix{StartedAt: time.Now()}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Minute
	}
	if opts.ArtifactsRoot == "" {
		opts.ArtifactsRoot = filepath.Join(os.TempDir(), "qawork-compat", time.Now().Format("20060102-150405"))
	}
	plan, err := effectivePlan(opts)
	if err != nil {
		return m, err
	}
	indices, err := automation.SelectIndices(plan, opts.CaseName)
	if err != nil {
		return m, err
	}
	devices, err := onlineDevices(ctx, opts.ADB, opts.Devices)
	if err != nil {
		return m, err
	}
	if len(devices) == 0 {
		return m, fmt.Errorf("没有在线 Android 设备")
	}
	m.Devices = len(devices)
	m.Cases = len(indices)

	for di, serial := range devices {
		if opts.OnDevice != nil {
			opts.OnDevice(serial, di+1, len(devices))
		}
		info := inspectDevice(ctx, opts.ADB, serial)
		installOK := true
		installSkipped := opts.APKPath == ""
		installLog := ""
		if opts.APKPath != "" {
			out, err := opts.ADB.Exec(ctx, "-s", serial, "install", "-r", "-g", opts.APKPath)
			installLog = strings.TrimSpace(out)
			if err != nil || !strings.Contains(installLog, "Success") {
				installOK = false
				if installLog == "" && err != nil {
					installLog = err.Error()
				}
			}
		}
		for _, idx := range indices {
			row := runCase(ctx, opts, plan, idx, serial, info, installOK, installSkipped, installLog)
			m.Rows = append(m.Rows, row)
			if row.Failed {
				m.Failed = true
			}
		}
	}
	m.DurationSeconds = time.Since(m.StartedAt).Seconds()
	m.Root = opts.ArtifactsRoot
	writeMatrix(&m)
	return m, nil
}

func effectivePlan(opts Options) (*automation.Plan, error) {
	if opts.Plan != nil {
		if errs := opts.Plan.Validate(); len(errs) > 0 {
			return nil, &automation.ValidationError{Errors: errs}
		}
		return opts.Plan, nil
	}
	if strings.TrimSpace(opts.Package) == "" {
		return nil, fmt.Errorf("没有 qawork.yaml 时必须给 package")
	}
	return SmokePlan(opts.Package), nil
}

// SmokePlan is the no-yaml compatibility case: cold launch, 5 seconds idle,
// no perf gates. It answers "does this app run on this device", not "is it
// fast"; pass a plan for performance and behavior gates.
func SmokePlan(pkg string) *automation.Plan {
	zero := 0.0
	yes := true
	return &automation.Plan{
		Version: 1,
		Cases: []automation.Case{{
			Name: "smoke", Package: pkg, Interval: "1s", Warmup: "1s", Settle: "1s",
			Steps: []automation.Step{
				{Action: "launch", Cold: &yes},
				{Action: "wait", Seconds: 5},
			},
			Assert: automation.Assert{
				MinFPS: &zero, MaxJank: &zero, MaxCPU: &zero, MaxMem: &zero,
				MaxTemp: &zero, MaxFrameP95MS: &zero, MaxStartupMS: &zero,
			},
		}},
	}
}

func onlineDevices(ctx context.Context, adb perf.ADB, wanted []string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	all, err := adb.Devices(ctx)
	if err != nil {
		return nil, fmt.Errorf("adb 不可用: %w", err)
	}
	online := map[string]bool{}
	var out []string
	for _, d := range all {
		if d.State == "device" {
			online[d.Serial] = true
			out = append(out, d.Serial)
		}
	}
	if len(wanted) == 0 {
		return out, nil
	}
	var selected []string
	for _, s := range wanted {
		if online[s] {
			selected = append(selected, s)
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("指定的设备都不在线: %s", strings.Join(wanted, ", "))
	}
	return selected, nil
}

func inspectDevice(ctx context.Context, adb perf.ADB, serial string) DeviceInfo {
	info := DeviceInfo{Serial: serial}
	info.Model = shell(ctx, adb, serial, "getprop ro.product.model")
	info.Android = shell(ctx, adb, serial, "getprop ro.build.version.release")
	info.SDK = shell(ctx, adb, serial, "getprop ro.build.version.sdk")
	info.ABI = shell(ctx, adb, serial, "getprop ro.product.cpu.abi")
	info.Resolution = ParseWMSize(shell(ctx, adb, serial, "wm size"))
	return info
}

func shell(ctx context.Context, adb perf.ADB, serial, cmd string) string {
	out, err := adb.Shell(ctx, serial, cmd)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// ParseWMSize picks the override size when present, else the physical size.
func ParseWMSize(out string) string {
	physical, override := "", ""
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "Physical size:"):
			physical = strings.TrimSpace(strings.TrimPrefix(line, "Physical size:"))
		case strings.HasPrefix(line, "Override size:"):
			override = strings.TrimSpace(strings.TrimPrefix(line, "Override size:"))
		}
	}
	if override != "" {
		return override
	}
	return physical
}

func runCase(ctx context.Context, opts Options, plan *automation.Plan, idx int, serial string, info DeviceInfo, installOK, installSkipped bool, installLog string) Row {
	row := Row{
		DeviceInfo: info, Case: plan.Cases[idx].Name, Package: plan.Cases[idx].Package,
		Installed: installOK && !installSkipped, InstallSkipped: installSkipped, InstallLog: installLog,
	}
	if row.Case == "" {
		row.Case = row.Package
	}
	if !installOK {
		row.Failed = true
		row.Error = "安装失败"
		return row
	}

	// Only logs produced by this case count: clear, run, read.
	_, _ = opts.ADB.Shell(ctx, serial, "logcat -c")
	root := filepath.Join(opts.ArtifactsRoot, safe(serial), safe(row.Case))
	results := automation.Execute(ctx, automation.ExecuteOptions{
		ADB: opts.ADB, Plan: plan, Indices: []int{idx}, Serial: serial,
		ArtifactsRoot: root, Timeout: opts.Timeout,
	})
	dev := automation.NewADBDevice(opts.ADB, serial)
	out, _ := opts.ADB.Shell(ctx, serial, "logcat -d -v brief -t 2000")
	crash, anr := AnalyzeLogcat(out, row.Package)

	if len(results) != 1 {
		row.Failed = true
		row.Error = "没有拿到运行结果"
	} else {
		r := results[0]
		row.StartupMS = r.StartupMS
		row.FPSMean = r.Summary.FPSMean
		row.FPSMin = r.Summary.FPSMin
		row.FrameP95 = r.Summary.FrameP95
		row.JankPct = r.Summary.JankPct
		row.CPUMax = r.Summary.CPUMax
		row.MemMaxMB = r.Summary.MemMaxMB
		row.TempMax = r.Summary.TempMax
		row.Checks = r.Checks
		row.CSV = r.CSV
		row.ResultJSON = r.ResultJSON
		row.Artifacts = append(row.Artifacts, r.Artifacts...)
		row.FailedChecks = failedChecks(r.Checks)
		row.Error = r.Error
		row.Failed = r.Failed
	}
	row.Crash = crash
	row.ANR = anr
	if crash || anr {
		row.Failed = true
	}

	dir := filepath.Join(root, "compat")
	_ = os.MkdirAll(dir, 0o755)
	if out != "" {
		path := filepath.Join(dir, "logcat.txt")
		if err := os.WriteFile(path, []byte(out), 0o644); err == nil {
			row.Logcat = path
			row.Artifacts = append(row.Artifacts, path)
		}
	}
	if png, err := dev.Screenshot(ctx); err == nil {
		path := filepath.Join(dir, "device.png")
		if err := os.WriteFile(path, png, 0o644); err == nil {
			row.Screenshot = path
			row.Artifacts = append(row.Artifacts, path)
		}
	}
	return row
}

func failedChecks(checks []perf.Check) int {
	n := 0
	for _, c := range checks {
		if !c.OK {
			n++
		}
	}
	return n
}

// AnalyzeLogcat flags a crash or ANR only when the package is tied to the
// event: an ANR line names the package; a FATAL EXCEPTION is followed by a
// "Process: pkg" line. Unrelated crashes do not poison the result.
func AnalyzeLogcat(out, pkg string) (crash, anr bool) {
	if pkg == "" {
		return false, false
	}
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if strings.Contains(line, "ANR in ") && strings.Contains(line, pkg) {
			anr = true
		}
		if !strings.Contains(line, "FATAL EXCEPTION") {
			continue
		}
		if strings.Contains(line, pkg) {
			crash = true
			continue
		}
		for j := i + 1; j < len(lines) && j <= i+5; j++ {
			if strings.Contains(lines[j], "Process: ") && strings.Contains(lines[j], pkg) {
				crash = true
				break
			}
		}
	}
	return crash, anr
}

func safe(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "device"
	}
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}

// CSVHeader is the matrix column order.
var CSVHeader = []string{
	"serial", "model", "android", "sdk", "abi", "resolution", "case", "package",
	"installed", "install_skipped", "startup_ms", "fps_mean", "fps_min", "frame_p95_ms", "jank_pct",
	"cpu_max", "mem_max_mb", "temp_max", "failed_checks", "crash", "anr", "passed", "error",
	"csv", "logcat", "screenshot", "artifacts",
}

// WriteCSV writes the compatibility matrix.
func WriteCSV(w io.Writer, rows []Row) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(CSVHeader); err != nil {
		return err
	}
	for _, r := range rows {
		rec := []string{
			r.Serial, r.Model, r.Android, r.SDK, r.ABI, r.Resolution, r.Case, r.Package,
			strconv.FormatBool(r.Installed), strconv.FormatBool(r.InstallSkipped), f(r.StartupMS), f(r.FPSMean), f(r.FPSMin),
			f(r.FrameP95), f(r.JankPct), f(r.CPUMax), f(r.MemMaxMB), f(r.TempMax),
			strconv.Itoa(r.FailedChecks), strconv.FormatBool(r.Crash), strconv.FormatBool(r.ANR),
			strconv.FormatBool(!r.Failed), r.Error, r.CSV, r.Logcat, r.Screenshot, strings.Join(r.Artifacts, "|"),
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func f(v float64) string {
	if v == 0 {
		return "0"
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}

func writeMatrix(m *Matrix) {
	if m.Root == "" {
		return
	}
	if err := os.MkdirAll(m.Root, 0o755); err != nil {
		return
	}
	csvPath := filepath.Join(m.Root, "compat-matrix.csv")
	fcsv, err := os.Create(csvPath)
	if err == nil {
		if WriteCSV(fcsv, m.Rows) == nil {
			m.CSV = csvPath
		}
		fcsv.Close()
	}
	jsonPath := filepath.Join(m.Root, "compat-matrix.json")
	if b, err := json.MarshalIndent(m, "", "  "); err == nil {
		if os.WriteFile(jsonPath, b, 0o644) == nil {
			m.JSON = jsonPath
		}
	}
}
