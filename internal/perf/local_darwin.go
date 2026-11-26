//go:build darwin

package perf

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LocalProcess is one process of this Mac, the target of a PC capture.
type LocalProcess struct {
	PID  uint32
	Exe  string
	Path string
}

// FindLocalProcesses lists processes whose executable name matches name,
// with or without ".exe".
func FindLocalProcesses(name string) ([]LocalProcess, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("未填写进程名")
	}
	out, err := exec.Command("ps", "-axo", "pid=,comm=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	var procs []LocalProcess
	for _, raw := range strings.Split(string(out), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.ParseUint(fields[0], 10, 32)
		if err != nil {
			continue
		}
		path := strings.Join(fields[1:], " ")
		base := filepath.Base(path)
		if !strings.EqualFold(base, name) && !strings.EqualFold(base, strings.TrimSuffix(name, ".exe")) {
			continue
		}
		procs = append(procs, LocalProcess{PID: uint32(pid), Exe: base, Path: path})
	}
	return procs, nil
}

// LocalSampler measures one Mac process: CPU from ps cumulative time,
// memory from ps RSS. macOS exposes no frame rate to an outside process
// without a Present hook, so FPS stays 0 and the note says why.
type LocalSampler struct {
	process LocalProcess
	prevAt  time.Time
	prevCPU float64
}

// NewLocalSampler opens the first process named exe.
func NewLocalSampler(exe string) (*LocalSampler, error) {
	procs, err := FindLocalProcesses(exe)
	if err != nil {
		return nil, err
	}
	if len(procs) == 0 {
		return nil, fmt.Errorf("未找到进程 %s", exe)
	}
	return NewLocalSamplerPID(procs[0].PID)
}

// NewLocalSamplerPID watches process pid.
func NewLocalSamplerPID(pid uint32) (*LocalSampler, error) {
	if pid == 0 {
		return nil, fmt.Errorf("PID 无效")
	}
	return &LocalSampler{process: LocalProcess{PID: pid}}, nil
}

// Name labels the target.
func (s *LocalSampler) Name() string {
	return fmt.Sprintf("本机进程 %s（PID %d）", s.process.Exe, s.process.PID)
}

// Start reads the process times once, so the first interval has a baseline.
func (s *LocalSampler) Start(ctx context.Context) error {
	cpu, err := s.cpuSeconds(ctx)
	if err != nil {
		return err
	}
	s.prevCPU = cpu
	s.prevAt = time.Now()
	return nil
}

// Sample takes one interval's measurements.
func (s *LocalSampler) Sample(ctx context.Context) (Sample, error) {
	now := time.Now()
	cpu, err := s.cpuSeconds(ctx)
	if err != nil {
		return Sample{At: now}, err
	}
	sample := Sample{At: now, Note: "PC 进程（无帧率）"}
	dt := now.Sub(s.prevAt).Seconds()
	if !s.prevAt.IsZero() && dt > 0.2 {
		delta := cpu - s.prevCPU
		if delta < 0 {
			delta = 0
		}
		sample.CPU = round2(clamp(delta/dt*100, 0, 100))
	}
	s.prevCPU = cpu
	s.prevAt = now
	sample.MemMB = round2(s.rssMB(ctx))
	return sample, nil
}

// Stop releases nothing on macOS; kept for the Sampler interface.
func (s *LocalSampler) Stop() error { return nil }

func (s *LocalSampler) cpuSeconds(ctx context.Context) (float64, error) {
	out, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(int(s.process.PID)), "-o", "time=").Output()
	if err != nil {
		return 0, fmt.Errorf("读取进程时间: %w", err)
	}
	return ParsePSTime(string(out)), nil
}

func (s *LocalSampler) rssMB(ctx context.Context) float64 {
	out, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(int(s.process.PID)), "-o", "rss=").Output()
	if err != nil {
		return 0
	}
	kb, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return kb / 1024
}
