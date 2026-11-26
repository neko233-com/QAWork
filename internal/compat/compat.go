// Package compat runs the same scenario across every connected Android
// device and produces one matrix: device, OS, install, startup, perf,
// crash/ANR, and pass/fail. It is the batch compatibility mode behind
// `qawork compat`, the GUI panel, and the MCP tool.
package compat

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html/template"
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
	Repeat        int           // per case; default 1
	ResetData     bool          // pm clear before each case run
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
	Case            string       `json:"case"`
	Run             int          `json:"run"`
	Package         string       `json:"package"`
	DurationSeconds float64      `json:"duration_seconds,omitempty"`
	Reset           bool         `json:"reset,omitempty"`
	Installed       bool         `json:"installed"`
	InstallSkipped  bool         `json:"install_skipped,omitempty"`
	InstallLog      string       `json:"install_log,omitempty"`
	StartupMS       float64      `json:"startup_ms,omitempty"`
	FPSMean         float64      `json:"fps_mean,omitempty"`
	FPSMin          float64      `json:"fps_min,omitempty"`
	FrameP95        float64      `json:"frame_p95_ms,omitempty"`
	JankPct         float64      `json:"jank_pct,omitempty"`
	CPUMax          float64      `json:"cpu_max,omitempty"`
	MemMaxMB        float64      `json:"mem_max_mb,omitempty"`
	TempMax         float64      `json:"temp_max,omitempty"`
	Checks          []perf.Check `json:"checks,omitempty"`
	FailedChecks    int          `json:"failed_checks"`
	Crash           bool         `json:"crash"`
	ANR             bool         `json:"anr"`
	Failed          bool         `json:"failed"`
	Error           string       `json:"error,omitempty"`
	CSV             string       `json:"csv,omitempty"`
	ResultJSON      string       `json:"result_json,omitempty"`
	Logcat          string       `json:"logcat,omitempty"`
	Screenshot      string       `json:"screenshot,omitempty"`
	Artifacts       []string     `json:"artifacts,omitempty"`
}

// Matrix is the whole batch, with evidence paths.
type Matrix struct {
	StartedAt       time.Time `json:"started_at"`
	DurationSeconds float64   `json:"duration_seconds"`
	Devices         int       `json:"devices"`
	Cases           int       `json:"cases"`
	Runs            int       `json:"runs"`
	Rows            []Row     `json:"rows"`
	Failed          bool      `json:"failed"`
	CSV             string    `json:"csv,omitempty"`
	JSON            string    `json:"json,omitempty"`
	HTML            string    `json:"html,omitempty"`
	JUnit           string    `json:"junit,omitempty"`
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
	repeat := opts.Repeat
	if repeat <= 0 {
		repeat = 1
	}
	if repeat > 100 {
		repeat = 100
	}
	m.Devices = len(devices)
	m.Cases = len(indices)
	m.Runs = repeat

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
		for run := 1; run <= repeat; run++ {
			for _, idx := range indices {
				row := runCase(ctx, opts, plan, idx, serial, run, info, installOK, installSkipped, installLog)
				m.Rows = append(m.Rows, row)
				if row.Failed {
					m.Failed = true
				}
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
	p := SmokePlan(opts.Package)
	if errs := p.Validate(); len(errs) > 0 {
		return nil, &automation.ValidationError{Errors: errs}
	}
	return p, nil
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

func runCase(ctx context.Context, opts Options, plan *automation.Plan, idx int, serial string, run int, info DeviceInfo, installOK, installSkipped bool, installLog string) Row {
	row := Row{
		DeviceInfo: info, Case: plan.Cases[idx].Name, Run: run, Package: plan.Cases[idx].Package,
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

	root := filepath.Join(opts.ArtifactsRoot, safe(serial), safe(row.Case))
	if opts.Repeat > 1 {
		root = filepath.Join(root, fmt.Sprintf("run-%02d", run))
	}
	if opts.ResetData {
		out, err := opts.ADB.Shell(ctx, serial, "pm clear "+row.Package)
		row.Reset = true
		if err != nil || !strings.Contains(strings.ToLower(out), "success") {
			row.Failed = true
			msg := strings.TrimSpace(out)
			if err != nil {
				if msg != "" {
					msg += "；"
				}
				msg += err.Error()
			}
			row.Error = "pm clear 失败: " + msg
			return row
		}
	}

	// Only logs produced by this case count: clear, run, read.
	_, _ = opts.ADB.Shell(ctx, serial, "logcat -c")
	results := automation.Execute(ctx, automation.ExecuteOptions{
		ADB: opts.ADB, Plan: plan, Indices: []int{idx}, Serial: serial,
		ArtifactsRoot: root, Timeout: opts.Timeout,
	})
	dev := automation.NewADBDevice(opts.ADB, serial)
	out, _ := opts.ADB.Shell(ctx, serial, "logcat -d -v brief -t 2000")
	crash, anr := perf.AnalyzeLogcat(out, row.Package)

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
		row.DurationSeconds = r.DurationSeconds
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
	"serial", "model", "android", "sdk", "abi", "resolution", "case", "run", "package",
	"installed", "install_skipped", "reset", "startup_ms", "fps_mean", "fps_min", "frame_p95_ms", "jank_pct",
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
			r.Serial, r.Model, r.Android, r.SDK, r.ABI, r.Resolution, r.Case, strconv.Itoa(r.Run), r.Package,
			strconv.FormatBool(r.Installed), strconv.FormatBool(r.InstallSkipped), strconv.FormatBool(r.Reset), f(r.StartupMS), f(r.FPSMean), f(r.FPSMin),
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
	if htmlPath, err := writeHTML(m); err == nil {
		m.HTML = htmlPath
	}
	if junitPath, err := writeJUnit(m); err == nil {
		m.JUnit = junitPath
	}
	jsonPath := filepath.Join(m.Root, "compat-matrix.json")
	if b, err := json.MarshalIndent(m, "", "  "); err == nil {
		if os.WriteFile(jsonPath, b, 0o644) == nil {
			m.JSON = jsonPath
		}
	}
}

type htmlView struct {
	*Matrix
	Passed, Failed, Crashes, ANRs int
}

func (v htmlView) Rel(p string) string {
	if p == "" {
		return ""
	}
	rel, err := filepath.Rel(v.Root, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(p)
	}
	return filepath.ToSlash(rel)
}

func writeHTML(m *Matrix) (string, error) {
	path := filepath.Join(m.Root, "compat-report.html")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data := htmlView{Matrix: m}
	for _, r := range m.Rows {
		if r.Failed {
			data.Failed++
		} else {
			data.Passed++
		}
		if r.Crash {
			data.Crashes++
		}
		if r.ANR {
			data.ANRs++
		}
	}
	if err := compatHTML.Execute(f, data); err != nil {
		return "", err
	}
	return path, nil
}

var compatHTML = template.Must(template.New("compat").Funcs(template.FuncMap{
	"fmtf": func(v float64) string {
		if v == 0 {
			return "—"
		}
		return strconv.FormatFloat(v, 'f', 1, 64)
	},
}).Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>QAWork 兼容矩阵</title>
<style>
body{font:14px/1.5 system-ui,"Segoe UI",sans-serif;margin:24px;color:#172033;background:#f6f8fb}
h1{font-size:22px;margin:0 0 4px} .sub{color:#667085;margin-bottom:18px}
.cards{display:flex;gap:12px;margin:16px 0}.card{background:#fff;border:1px solid #e4e7ec;border-radius:10px;padding:12px 18px;min-width:120px}
.card b{display:block;font-size:22px}.bad{color:#b42318}.ok{color:#067647}
table{border-collapse:collapse;width:100%;background:#fff;border:1px solid #e4e7ec;border-radius:10px;overflow:hidden}
th,td{padding:8px 10px;border-bottom:1px solid #eef1f5;text-align:left;vertical-align:top}th{background:#f9fafb;font-weight:600;white-space:nowrap}
tr.fail td{background:#fff6f5}.tag{font-size:12px;padding:2px 8px;border-radius:99px;background:#e8f5ee;color:#067647}.tag.fail{background:#fee4e2;color:#b42318}
a{color:#175cd3;text-decoration:none}a:hover{text-decoration:underline}.muted{color:#98a2b3}
</style></head><body>
<h1>QAWork 兼容矩阵</h1>
<div class="sub">{{.StartedAt.Format "2006-01-02 15:04:05"}} · {{.Devices}} 台设备 × {{.Cases}} 个 case × {{.Runs}} 轮 · {{fmtf .DurationSeconds}}s</div>
<div class="cards">
<div class="card"><b class="ok">{{.Passed}}</b>通过</div>
<div class="card"><b class="bad">{{.Failed}}</b>失败</div>
<div class="card"><b>{{.Crashes}}</b>Crash</div>
<div class="card"><b>{{.ANRs}}</b>ANR</div>
</div>
<table><thead><tr><th>设备</th><th>系统</th><th>Case</th><th>轮次</th><th>安装</th><th>启动 ms</th><th>FPS</th><th>掉帧%</th><th>CPU%</th><th>内存 MB</th><th>Crash</th><th>ANR</th><th>结果</th><th>证据</th></tr></thead><tbody>
{{range .Rows}}
<tr class="{{if .Failed}}fail{{end}}">
<td><b>{{.Model}}</b><br><span class="muted">{{.Serial}}</span></td>
<td>{{.Android}} / SDK {{.SDK}}<br><span class="muted">{{.ABI}} · {{.Resolution}}</span></td>
<td>{{.Case}}</td><td>{{.Run}}</td>
<td>{{if .InstallSkipped}}跳过{{else if .Installed}}成功{{else}}失败{{end}}{{if .Reset}}<br><span class="muted">已清数据</span>{{end}}</td>
<td>{{fmtf .StartupMS}}</td><td>{{fmtf .FPSMean}}</td><td>{{fmtf .JankPct}}</td><td>{{fmtf .CPUMax}}</td><td>{{fmtf .MemMaxMB}}</td>
<td>{{if .Crash}}<span class="bad">是</span>{{else}}否{{end}}</td>
<td>{{if .ANR}}<span class="bad">是</span>{{else}}否{{end}}</td>
<td><span class="tag {{if .Failed}}fail{{end}}">{{if .Failed}}失败{{else}}通过{{end}}</span>{{if .Error}}<div class="muted">{{.Error}}</div>{{end}}</td>
<td>{{if .CSV}}<a href="{{$.Rel .CSV}}">CSV</a>{{end}} {{if .ResultJSON}}<a href="{{$.Rel .ResultJSON}}">JSON</a>{{end}} {{if .Logcat}}<a href="{{$.Rel .Logcat}}">logcat</a>{{end}} {{if .Screenshot}}<a href="{{$.Rel .Screenshot}}">截图</a>{{end}}</td>
</tr>{{end}}
</tbody></table>
<p class="muted">生成：{{.StartedAt.Format "2006-01-02 15:04:05"}} · 只有设备上真实执行的行会出现在矩阵里。</p>
</body></html>`))

func writeJUnit(m *Matrix) (string, error) {
	path := filepath.Join(m.Root, "compat-junit.xml")
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	failures := 0
	for _, r := range m.Rows {
		if r.Failed {
			failures++
		}
	}
	fmt.Fprintf(&b, `<testsuite name="QAWork compat" tests="%d" failures="%d" time="%.1f">`+"\n", len(m.Rows), failures, m.DurationSeconds)
	for _, r := range m.Rows {
		fmt.Fprintf(&b, `  <testcase classname="%s" name="%s#%d" time="%.1f">`+"\n",
			xmlEscape(r.Serial), xmlEscape(r.Case), r.Run, r.DurationSeconds)
		if r.Failed {
			msg := xmlEscape(r.Error)
			if msg == "" {
				msg = "checks/crash"
			}
			fmt.Fprintf(&b, `    <failure message="%s">%s</failure>`+"\n", msg, msg)
		}
		b.WriteString("  </testcase>\n")
	}
	b.WriteString("</testsuite>\n")
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
