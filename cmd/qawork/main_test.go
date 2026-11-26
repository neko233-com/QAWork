package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/neko233-com/QAWork/internal/perf"
)

func TestPrintSummaryWithEngine(t *testing.T) {
	s := perf.Summary{
		Samples: 5, DurationSeconds: 4, FPSMean: 55, FPSMin: 40,
		FrameP50: 18, FrameP95: 23, FrameP99: 25, FrameSamples: 5,
		JankPct: 2, CPUMax: 50, CPUMean: 30, MemMaxMB: 400, TempMax: 38,
		Engine: perf.EngineUnity, EngineLabel: "Unity",
		MainThread: "UnityMain", MainCPUMax: 42, RenderThread: "UnityGfxDeviceW", RenderCPUMax: 21,
	}
	checks := perf.DefaultThresholds().Evaluate(s)
	var buf bytes.Buffer
	printSummary(&buf, "Android 123 com.game", s, checks, 60)
	out := buf.String()
	for _, want := range []string{"引擎: Unity", "UnityMain", "UnityGfxDeviceW", "平均帧率", "门禁"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
}

func TestPrintSummaryWithoutFrames(t *testing.T) {
	s := perf.Summary{Samples: 2, CPUMax: 20, MemMaxMB: 100}
	var buf bytes.Buffer
	printSummary(&buf, "本机进程 x.exe", s, nil, 60)
	out := buf.String()
	if strings.Contains(out, "引擎") {
		t.Errorf("no engine line expected:\n%s", out)
	}
	if !strings.Contains(out, "采样: 2") {
		t.Errorf("summary:\n%s", out)
	}
}
