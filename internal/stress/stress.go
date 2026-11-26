// Package stress drives a real adb monkey loop while sampling performance,
// then ties crashes/ANRs to the package under test. It is the long-random
// stability run that compatibility matrices deliberately do not do.
package stress

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/neko233-com/QAWork/internal/adbkit"
	"github.com/neko233-com/QAWork/internal/automation"
	"github.com/neko233-com/QAWork/internal/perf"
)

// Options configures one stress run.
type Options struct {
	ADB           perf.ADB
	Serial        string
	Package       string
	Duration      time.Duration // default 5m; also bounded by Events
	Events        int           // default 3000
	ThrottleMS    int           // default 300
	Interval      time.Duration // default 1s
	ArtifactsRoot string
	OnSample      func(perf.Sample)
}

// Result is one stress run's evidence. It never claims a pass when monkey or
// logcat could not be read.
type Result struct {
	Serial          string       `json:"serial"`
	Package         string       `json:"package"`
	StartedAt       time.Time    `json:"started_at"`
	DurationSeconds float64      `json:"duration_seconds"`
	Events          int          `json:"events"`
	ThrottleMS      int          `json:"throttle_ms"`
	Samples         int          `json:"samples"`
	Summary         perf.Summary `json:"summary"`
	Crash           bool         `json:"crash"`
	ANR             bool         `json:"anr"`
	Failed          bool         `json:"failed"`
	Error           string       `json:"error,omitempty"`
	MonkeyOutput    string       `json:"monkey_output,omitempty"`
	MonkeyFile      string       `json:"monkey_file,omitempty"`
	Logcat          string       `json:"logcat,omitempty"`
	CSV             string       `json:"csv,omitempty"`
	ResultJSON      string       `json:"result_json,omitempty"`
	Artifacts       []string     `json:"artifacts,omitempty"`
}

// Run executes one stress case. A device or launch failure is returned in the
// Result and err only for setup problems that make a run meaningless.
func Run(ctx context.Context, opts Options) (Result, error) {
	res := Result{Package: opts.Package, StartedAt: time.Now()}
	if !adbkit.ValidPackage(opts.Package) {
		return res, fmt.Errorf("包名无效: %q", opts.Package)
	}
	if opts.Duration <= 0 {
		opts.Duration = 5 * time.Minute
	}
	if opts.Duration > 2*time.Hour {
		opts.Duration = 2 * time.Hour
	}
	if opts.Events <= 0 {
		opts.Events = 3000
	}
	if opts.Events > 1000000 {
		opts.Events = 1000000
	}
	if opts.ThrottleMS < 0 {
		opts.ThrottleMS = 0
	}
	if opts.ThrottleMS > 5000 {
		opts.ThrottleMS = 5000
	}
	if opts.Interval < 200*time.Millisecond {
		opts.Interval = time.Second
	}
	res.Events = opts.Events
	res.ThrottleMS = opts.ThrottleMS
	if opts.ArtifactsRoot == "" {
		opts.ArtifactsRoot = filepath.Join(os.TempDir(), "qawork-stress", time.Now().Format("20060102-150405"))
	}
	serial := opts.Serial
	if serial == "" {
		serial = firstOnline(ctx, opts.ADB)
	}
	if serial == "" {
		return res, fmt.Errorf("没有在线 Android 设备")
	}
	res.Serial = serial
	if err := os.MkdirAll(opts.ArtifactsRoot, 0o755); err != nil {
		return res, err
	}

	dev := automation.NewADBDevice(opts.ADB, serial)
	if _, err := automation.Launch(ctx, dev, opts.Package, "", false); err != nil {
		res.Error = "启动失败: " + err.Error()
		res.Failed = true
		save(&res, nil, opts.ArtifactsRoot)
		res.saveJSON(opts.ArtifactsRoot)
		return res, nil
	}

	runCtx, cancel := context.WithTimeout(ctx, opts.Duration)
	defer cancel()
	_, _ = opts.ADB.Shell(runCtx, serial, "logcat -c")

	type captureResult struct {
		samples []perf.Sample
		err     error
	}
	captureCh := make(chan captureResult, 1)
	captureCtx, stopCapture := context.WithCancel(runCtx)
	sampler := perf.NewAndroidSampler(opts.ADB, serial, opts.Package)
	go func() {
		samples, err := perf.RunCapture(captureCtx, sampler, opts.Interval, opts.OnSample)
		captureCh <- captureResult{samples: samples, err: err}
	}()

	out, monkeyErr := (adbkit.Client{ADB: opts.ADB}).Monkey(runCtx, serial, opts.Package, opts.Events, opts.ThrottleMS)
	stopCapture()
	capRes := <-captureCh
	if monkeyErr != nil && runCtx.Err() != context.DeadlineExceeded {
		res.Error = "monkey: " + monkeyErr.Error()
	}
	res.MonkeyOutput = excerpt(out, 500)
	res.Samples = len(capRes.samples)
	res.Summary = perf.Summarize(capRes.samples)
	if capRes.err != nil && runCtx.Err() != context.DeadlineExceeded && res.Error == "" {
		res.Error = "采样: " + capRes.err.Error()
	}
	if len(capRes.samples) == 0 && res.Error == "" {
		res.Error = "没有采到样本"
	}
	if strings.Contains(out, "No activities found") && res.Error == "" {
		res.Error = "monkey 找不到可启动的 Activity"
	}

	logCtx, logCancel := context.WithTimeout(context.Background(), 15*time.Second)
	logText, logErr := (adbkit.Client{ADB: opts.ADB}).LogcatDump(logCtx, serial, 5000, false)
	logCancel()
	if logErr != nil && res.Error == "" {
		res.Error = "读取 logcat: " + logErr.Error()
	}
	res.Crash, res.ANR = perf.AnalyzeLogcat(logText, opts.Package)
	res.DurationSeconds = time.Since(res.StartedAt).Seconds()
	res.Failed = res.Error != "" || res.Crash || res.ANR || len(capRes.samples) == 0
	save(&res, capRes.samples, opts.ArtifactsRoot)
	if logText != "" {
		path := filepath.Join(opts.ArtifactsRoot, "logcat.txt")
		if err := os.WriteFile(path, []byte(logText), 0o644); err == nil {
			res.Logcat = path
			res.Artifacts = append(res.Artifacts, path)
		}
	}
	if out != "" {
		path := filepath.Join(opts.ArtifactsRoot, "monkey.txt")
		if err := os.WriteFile(path, []byte(out), 0o644); err == nil {
			res.MonkeyFile = path
			res.Artifacts = append(res.Artifacts, path)
		}
	}
	res.saveJSON(opts.ArtifactsRoot)
	return res, nil
}

func (r *Result) saveJSON(root string) {
	r.ResultJSON = filepath.Join(root, "result.json")
	b, err := json.MarshalIndent(r, "", "  ")
	if err == nil {
		_ = os.WriteFile(r.ResultJSON, b, 0o644)
	}
}

func save(res *Result, samples []perf.Sample, root string) {
	if len(samples) > 0 {
		path := filepath.Join(root, "samples.csv")
		if err := perf.SaveCSV(path, samples); err == nil {
			res.CSV = path
			res.Artifacts = append(res.Artifacts, path)
		}
	}
	res.ResultJSON = filepath.Join(root, "result.json")
	res.Artifacts = append(res.Artifacts, res.ResultJSON)
}

func excerpt(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func firstOnline(ctx context.Context, adb perf.ADB) string {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	devices, err := adb.Devices(ctx)
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
