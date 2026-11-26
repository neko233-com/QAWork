package automation

import (
	"context"
	"fmt"
	"time"

	"github.com/neko233-com/QAWork/internal/perf"
)

// ExecuteOptions runs one or more cases over Android devices. It is the
// shared orchestration behind the CLI, MCP and GUI, so exit codes and
// artifacts stay identical.
type ExecuteOptions struct {
	ADB           perf.ADB
	Plan          *Plan
	Indices       []int
	Serial        string // overrides every case's serial
	ArtifactsRoot string
	Timeout       time.Duration // per case
	OnStart       func(index int, c Case)
	OnResult      func(Result)
}

// Execute runs the selected cases sequentially and returns each result.
// A missing device or a broken case becomes a failed Result, not an abort
// of the whole set.
func Execute(ctx context.Context, opts ExecuteOptions) []Result {
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Minute
	}
	var results []Result
	for _, idx := range opts.Indices {
		if idx < 0 || idx >= len(opts.Plan.Cases) {
			continue
		}
		cfg, err := opts.Plan.Resolve(idx)
		if err != nil {
			results = append(results, Result{Case: opts.Plan.Cases[idx].Name, Failed: true, Error: err.Error()})
			continue
		}
		serial := opts.Serial
		if serial == "" {
			serial = cfg.Serial
		}
		if serial == "" {
			serial = firstOnlineSerial(ctx, opts.ADB)
		}
		if serial == "" {
			results = append(results, Result{
				Case: cfg.Case.Name, Package: cfg.Case.Package, Failed: true,
				Error: "没有在线 Android 设备（先 qawork devices 或开模拟器）",
			})
			continue
		}
		if opts.OnStart != nil {
			opts.OnStart(idx, cfg.Case)
		}
		dev := NewADBDevice(opts.ADB, serial)
		sampler := perf.NewAndroidSampler(opts.ADB, serial, cfg.Case.Package)
		runner, err := NewRunner(opts.Plan, idx, serial, dev, sampler, opts.ArtifactsRoot)
		if err != nil {
			results = append(results, Result{Case: cfg.Case.Name, Serial: serial, Package: cfg.Case.Package, Failed: true, Error: err.Error()})
			continue
		}
		caseCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
		res := runner.Run(caseCtx)
		cancel()
		results = append(results, res)
		if opts.OnResult != nil {
			opts.OnResult(res)
		}
	}
	return results
}

// firstOnlineSerial returns the first adb device in "device" state.
func firstOnlineSerial(ctx context.Context, adb perf.ADB) string {
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

// SelectIndices resolves a case name to plan indices; empty selects all.
func SelectIndices(p *Plan, name string) ([]int, error) {
	if name == "" {
		idx := make([]int, len(p.Cases))
		for i := range idx {
			idx[i] = i
		}
		return idx, nil
	}
	for i, c := range p.Cases {
		if c.Name == name {
			return []int{i}, nil
		}
	}
	return nil, fmt.Errorf("没有 case %q", name)
}
