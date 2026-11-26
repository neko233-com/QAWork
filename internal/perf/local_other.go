//go:build !windows

package perf

import (
	"context"
	"errors"
)

// ErrUnsupportedLocal is what the PC process sampler returns off Windows.
var ErrUnsupportedLocal = errors.New("本机进程采集仅支持 Windows")

// LocalProcess is one process of this machine; off Windows it is never
// collected.
type LocalProcess struct {
	PID  uint32
	Exe  string
	Path string
}

// FindLocalProcesses is unavailable off Windows.
func FindLocalProcesses(string) ([]LocalProcess, error) { return nil, ErrUnsupportedLocal }

// LocalSampler is a stub off Windows; QAWork's PC capture is Windows-first.
type LocalSampler struct{}

// NewLocalSampler is unavailable off Windows.
func NewLocalSampler(string) (*LocalSampler, error) { return nil, ErrUnsupportedLocal }

// NewLocalSamplerPID is unavailable off Windows.
func NewLocalSamplerPID(uint32) (*LocalSampler, error) { return nil, ErrUnsupportedLocal }

// Name labels the stub target.
func (s *LocalSampler) Name() string { return "本机进程（不支持）" }

// Start fails off Windows.
func (s *LocalSampler) Start(context.Context) error { return ErrUnsupportedLocal }

// Sample fails off Windows.
func (s *LocalSampler) Sample(context.Context) (Sample, error) {
	return Sample{}, ErrUnsupportedLocal
}

// Stop does nothing off Windows.
func (s *LocalSampler) Stop() error { return nil }
