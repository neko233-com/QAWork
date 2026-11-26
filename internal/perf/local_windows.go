//go:build windows

package perf

import (
	"context"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// LocalProcess is one process of this machine, the target of a PC capture.
type LocalProcess struct {
	PID  uint32
	Exe  string
	Path string
}

// FindLocalProcesses lists the processes whose executable matches name,
// with or without ".exe".
func FindLocalProcesses(name string) ([]LocalProcess, error) {
	if name == "" {
		return nil, fmt.Errorf("未填写进程名")
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	var out []LocalProcess
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		exe := windows.UTF16ToString(entry.ExeFile[:])
		want := name
		if len(want) < 4 || (len(want) >= 4 && want[len(want)-4:] != ".exe") {
			want += ".exe"
		}
		if !equalFold(exe, name) && !equalFold(exe, want) {
			continue
		}
		out = append(out, LocalProcess{PID: entry.ProcessID, Exe: exe})
	}
	return out, nil
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

var (
	psapi                    = windows.NewLazySystemDLL("psapi.dll")
	procGetProcessMemoryInfo = psapi.NewProc("GetProcessMemoryInfo")
)

type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
	PrivateUsage               uintptr
}

func workingSetSize(h windows.Handle) uint64 {
	var mc processMemoryCounters
	mc.CB = uint32(unsafe.Sizeof(mc))
	r, _, _ := procGetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&mc)), uintptr(mc.CB))
	if r == 0 {
		return 0
	}
	return uint64(mc.WorkingSetSize)
}

// LocalSampler measures one process of this machine: CPU from its kernel
// and user times, memory from its working set. Windows offers no frame
// rate to an outside process without a Present hook, so FPS stays 0 and
// the sample says why.
type LocalSampler struct {
	process LocalProcess
	handle  windows.Handle
	prevAt  time.Time
	prevNS  int64
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

// NewLocalSamplerPID opens process pid.
func NewLocalSamplerPID(pid uint32) (*LocalSampler, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return nil, fmt.Errorf("打开进程 %d: %w", pid, err)
	}
	return &LocalSampler{process: LocalProcess{PID: pid}, handle: h}, nil
}

// Name labels the target.
func (s *LocalSampler) Name() string {
	return fmt.Sprintf("本机进程 %s（PID %d）", s.process.Exe, s.process.PID)
}

// Start reads the process times once, so the first interval has a baseline.
func (s *LocalSampler) Start(ctx context.Context) error {
	if _, err := s.times(); err != nil {
		return err
	}
	s.prevAt = time.Now()
	return nil
}

// Stop closes the process handle.
func (s *LocalSampler) Stop() error {
	if s.handle != 0 {
		windows.CloseHandle(s.handle)
		s.handle = 0
	}
	return nil
}

func (s *LocalSampler) times() (int64, error) {
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(s.handle, &creation, &exit, &kernel, &user); err != nil {
		return 0, fmt.Errorf("读取进程时间: %w", err)
	}
	return kernel.Nanoseconds() + user.Nanoseconds(), nil
}

// Sample takes one interval's measurements.
func (s *LocalSampler) Sample(ctx context.Context) (Sample, error) {
	now := time.Now()
	cpuNS, err := s.times()
	if err != nil {
		return Sample{At: now}, err
	}
	sample := Sample{At: now, Note: "PC 进程（无帧率）"}
	dt := now.Sub(s.prevAt).Seconds()
	if !s.prevAt.IsZero() && dt > 0.2 {
		delta := cpuNS - s.prevNS
		if delta < 0 {
			delta = 0
		}
		sample.CPU = round2(clamp(float64(delta)/1e9/dt*100, 0, 100))
	}
	s.prevNS = cpuNS
	s.prevAt = now
	sample.MemMB = round2(float64(workingSetSize(s.handle)) / (1024 * 1024))
	return sample, nil
}
