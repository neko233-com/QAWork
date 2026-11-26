package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/neko233-com/QAWork/internal/perf"
)

// StepResult is one executed step, with enough detail to debug a failed run.
type StepResult struct {
	Index      int     `json:"index"`
	Action     string  `json:"action"`
	Detail     string  `json:"detail,omitempty"`
	DurationMS float64 `json:"duration_ms"`
	Error      string  `json:"error,omitempty"`
	Artifact   string  `json:"artifact,omitempty"`
}

// Result is one case's automated verification: what ran, what was measured,
// what passed, and where the evidence lives.
type Result struct {
	Case            string       `json:"case"`
	Serial          string       `json:"serial"`
	Package         string       `json:"package"`
	StartedAt       time.Time    `json:"started_at"`
	DurationSeconds float64      `json:"duration_seconds"`
	StartupMS       float64      `json:"startup_ms,omitempty"`
	Steps           []StepResult `json:"steps"`
	Summary         perf.Summary `json:"summary"`
	Checks          []perf.Check `json:"checks"`
	Failed          bool         `json:"failed"`
	Error           string       `json:"error,omitempty"`
	CSV             string       `json:"csv,omitempty"`
	ResultJSON      string       `json:"result_json,omitempty"`
	Artifacts       []string     `json:"artifacts,omitempty"`
}

// Runner executes one resolved case. Sampler and Device are injected so the
// runner can be unit-tested without a phone.
type Runner struct {
	Plan    *Plan
	Index   int
	Config  Config
	Device  Device
	Sampler perf.Sampler

	artifactsRoot string
	caseDir       string
}

// NewRunner resolves case idx and prepares its artifact directory.
func NewRunner(plan *Plan, idx int, serialOverride string, dev Device, sampler perf.Sampler, artifactsRoot string) (*Runner, error) {
	cfg, err := plan.Resolve(idx)
	if err != nil {
		return nil, err
	}
	if serialOverride != "" {
		cfg.Serial = serialOverride
	}
	if cfg.Serial == "" && dev != nil {
		cfg.Serial = dev.Serial()
	}
	if sampler == nil {
		return nil, fmt.Errorf("缺少采样器")
	}
	if dev == nil {
		return nil, fmt.Errorf("缺少设备")
	}
	if artifactsRoot == "" {
		artifactsRoot = filepath.Join(os.TempDir(), "qawork-automation")
	}
	name := safeName(cfg.Case.Name)
	if name == "" {
		name = "case"
	}
	r := &Runner{
		Plan: plan, Index: idx, Config: cfg, Device: dev, Sampler: sampler,
		artifactsRoot: artifactsRoot,
		caseDir:       filepath.Join(artifactsRoot, fmt.Sprintf("%s-%s", name, time.Now().Format("20060102-150405.000"))),
	}
	return r, nil
}

// ArtifactsDir is where this run writes its evidence.
func (r *Runner) ArtifactsDir() string { return r.caseDir }

// Run executes the case. It always returns a Result; execution failures are
// in Result.Error and Result.Failed, never hidden as a pass.
func (r *Runner) Run(ctx context.Context) (res Result) {
	res = Result{
		Case:      r.Config.Case.Name,
		Serial:    r.Config.Serial,
		Package:   r.Config.Case.Package,
		StartedAt: time.Now(),
	}
	if res.Case == "" {
		res.Case = r.Config.Case.Package
	}
	var (
		samples        []perf.Sample
		sampleErr      error
		mu             sync.Mutex
		samplerStarted bool
		sampleCancel   context.CancelFunc
		sampleDone     chan struct{}
	)
	defer func() {
		if sampleCancel != nil {
			sampleCancel()
			<-sampleDone
		}
		if samplerStarted {
			_ = r.Sampler.Stop()
		}
		mu.Lock()
		collected := append([]perf.Sample(nil), samples...)
		lastErr := sampleErr
		mu.Unlock()
		res.DurationSeconds = time.Since(res.StartedAt).Seconds()
		res.Summary = perf.Summarize(collected)
		res.Checks = evaluate(r.Config.Limits, res.Summary, res.StartupMS)
		res.Failed = res.Error != "" || perf.Failed(res.Checks) || len(collected) == 0
		if len(collected) == 0 && res.Error == "" {
			if lastErr != nil {
				res.Error = "没有采到样本: " + lastErr.Error()
			} else {
				res.Error = "没有采到样本"
			}
		}
		if res.Failed {
			r.captureFailure(ctx, &res)
		}
		r.save(&res, collected)
	}()

	if _, err := r.Device.Shell(ctx, "echo qawork"); err != nil {
		res.Error = "设备不可用: " + err.Error()
		return res
	}

	steps := r.Config.Case.Steps
	i := 0
	if len(steps) > 0 && steps[0].Action == "launch" {
		sr, launch, err := r.runLaunch(ctx, steps[0])
		res.Steps = append(res.Steps, sr)
		if launch.Measured {
			res.StartupMS = launch.StartupMS
		}
		if err != nil {
			res.Error = err.Error()
			return res
		}
		i = 1
	}

	if err := r.Sampler.Start(ctx); err != nil {
		res.Error = "采样器启动失败: " + err.Error()
		return res
	}
	samplerStarted = true
	sampleCtx, cancel := context.WithCancel(ctx)
	sampleCancel = cancel
	sampleDone = make(chan struct{})
	takeSample := func() {
		s, err := r.Sampler.Sample(sampleCtx)
		mu.Lock()
		if err != nil {
			sampleErr = err
		} else {
			samples = append(samples, s)
		}
		mu.Unlock()
	}
	go func() {
		defer close(sampleDone)
		ticker := time.NewTicker(r.Config.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-sampleCtx.Done():
				return
			case <-ticker.C:
				takeSample()
			}
		}
	}()
	// Take one sample immediately: a case with no waits must still produce
	// evidence instead of failing as "没有采到样本".
	takeSample()

	if err := sleepCtx(ctx, r.Config.Warmup); err != nil {
		res.Error = err.Error()
		return res
	}

	for ; i < len(steps); i++ {
		sr, err := r.runStep(ctx, i, steps[i])
		res.Steps = append(res.Steps, sr)
		if err != nil {
			res.Error = fmt.Sprintf("第 %d 步 %s: %v", i+1, steps[i].Action, err)
			return res
		}
	}
	if err := sleepCtx(ctx, r.Config.Settle); err != nil {
		res.Error = err.Error()
		return res
	}
	if ctx.Err() != nil {
		res.Error = "运行被取消"
	}
	return res
}

func (r *Runner) runLaunch(ctx context.Context, s Step) (StepResult, LaunchResult, error) {
	cold := s.Cold == nil || *s.Cold
	sr := StepResult{Action: "launch"}
	start := time.Now()
	lr, err := Launch(ctx, r.Device, r.Config.Case.Package, r.Config.Case.Activity, cold)
	sr.DurationMS = float64(time.Since(start).Milliseconds())
	if err != nil {
		sr.Error = err.Error()
		return sr, lr, err
	}
	sr.Detail = fmt.Sprintf("%s cold=%v", lr.Activity, cold)
	if lr.Measured {
		sr.Detail += fmt.Sprintf(" startup=%.0fms", lr.StartupMS)
	}
	return sr, lr, nil
}

func (r *Runner) runStep(ctx context.Context, index int, s Step) (StepResult, error) {
	sr := StepResult{Index: index, Action: s.Action}
	start := time.Now()
	defer func() { sr.DurationMS = float64(time.Since(start).Milliseconds()) }()
	switch s.Action {
	case "wait":
		d := stepDuration(s)
		if err := sleepCtx(ctx, d); err != nil {
			sr.Error = err.Error()
			return sr, err
		}
		sr.Detail = "等待 " + d.String()
	case "screenshot":
		png, err := r.Device.Screenshot(ctx)
		if err != nil {
			sr.Error = err.Error()
			return sr, err
		}
		if err := os.MkdirAll(r.caseDir, 0o755); err != nil {
			sr.Error = err.Error()
			return sr, err
		}
		path := filepath.Join(r.caseDir, fmt.Sprintf("step-%02d.png", index+1))
		if err := os.WriteFile(path, png, 0o644); err != nil {
			sr.Error = err.Error()
			return sr, err
		}
		sr.Artifact = path
		sr.Detail = path
	default:
		cmd, err := StepCommand(s)
		if err != nil {
			sr.Error = err.Error()
			return sr, err
		}
		if _, err := r.Device.Shell(ctx, cmd); err != nil {
			sr.Error = err.Error()
			return sr, err
		}
		sr.Detail = cmd
	}
	return sr, nil
}

func (r *Runner) captureFailure(ctx context.Context, res *Result) {
	sctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	png, err := r.Device.Screenshot(sctx)
	if err != nil {
		return
	}
	if err := os.MkdirAll(r.caseDir, 0o755); err != nil {
		return
	}
	path := filepath.Join(r.caseDir, "failure.png")
	if err := os.WriteFile(path, png, 0o644); err != nil {
		return
	}
	res.Artifacts = append(res.Artifacts, path)
}

func (r *Runner) save(res *Result, samples []perf.Sample) {
	if err := os.MkdirAll(r.caseDir, 0o755); err != nil {
		res.Artifacts = append(res.Artifacts, "artifacts: "+err.Error())
		return
	}
	if len(samples) > 0 {
		path := filepath.Join(r.caseDir, "samples.csv")
		if err := perf.SaveCSV(path, samples); err == nil {
			res.CSV = path
			res.Artifacts = append(res.Artifacts, path)
		}
	}
	path := filepath.Join(r.caseDir, "result.json")
	res.ResultJSON = path
	res.Artifacts = append(res.Artifacts, path)
	b, err := json.MarshalIndent(res, "", "  ")
	if err == nil {
		_ = os.WriteFile(path, b, 0o644)
	}
}

func evaluate(l Limits, s perf.Summary, startupMS float64) []perf.Check {
	checks := l.Thresholds.Evaluate(s)
	if l.MaxFrameP95 > 0 && s.FrameSamples > 0 {
		checks = append(checks, perf.Check{
			Name: "帧耗时 P95", Value: s.FrameP95, Limit: l.MaxFrameP95, Unit: "ms",
			OK: s.FrameP95 <= l.MaxFrameP95,
		})
	}
	if l.MaxStartupMS > 0 && startupMS > 0 {
		checks = append(checks, perf.Check{
			Name: "冷启动", Value: startupMS, Limit: l.MaxStartupMS, Unit: "ms",
			OK: startupMS <= l.MaxStartupMS,
		})
	}
	return checks
}

func stepDuration(s Step) time.Duration {
	if s.Seconds > 0 {
		return time.Duration(s.Seconds * float64(time.Second))
	}
	if s.MS > 0 {
		return time.Duration(s.MS) * time.Millisecond
	}
	return 0
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func safeName(s string) string {
	s = strings.TrimSpace(unsafeName.ReplaceAllString(s, "-"))
	s = strings.Trim(s, "-._")
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}
