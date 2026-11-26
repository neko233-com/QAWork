package compat

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseWMSize(t *testing.T) {
	cases := map[string]string{
		"Physical size: 1080x2400":                           "1080x2400",
		"Physical size: 1080x2400\nOverride size: 720x1600":  "720x1600",
		"Override size: 1080x1920\nPhysical size: 1080x2400": "1080x1920",
		"garbage":                      "",
		"Physical size: 1440x3200\r\n": "1440x3200",
	}
	for in, want := range cases {
		if got := ParseWMSize(in); got != want {
			t.Errorf("ParseWMSize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnalyzeLogcat(t *testing.T) {
	crashLog := `07-01 10:00:00.000 E AndroidRuntime: FATAL EXCEPTION: main
07-01 10:00:00.001 E AndroidRuntime: Process: com.example.game, PID: 1234
07-01 10:00:00.002 E AndroidRuntime: java.lang.RuntimeException: boom`
	crash, anr := AnalyzeLogcat(crashLog, "com.example.game")
	if !crash || anr {
		t.Errorf("crash=%v anr=%v", crash, anr)
	}

	otherCrash := `E AndroidRuntime: FATAL EXCEPTION: main
E AndroidRuntime: Process: com.other.app, PID: 1`
	if crash, _ := AnalyzeLogcat(otherCrash, "com.example.game"); crash {
		t.Error("unrelated crash must not count")
	}

	anrLog := `07-01 10:00:00 E ActivityManager: ANR in com.example.game (com.example.game/.Main)`
	if crash, anr := AnalyzeLogcat(anrLog, "com.example.game"); crash || !anr {
		t.Errorf("crash=%v anr=%v", crash, anr)
	}

	if crash, anr := AnalyzeLogcat("normal logs\n", "com.example"); crash || anr {
		t.Error("clean log flagged")
	}
	if crash, anr := AnalyzeLogcat(crashLog, ""); crash || anr {
		t.Error("empty package must not match")
	}
}

func TestSmokePlanHasNoGates(t *testing.T) {
	p := SmokePlan("com.example.game")
	if errs := p.Validate(); len(errs) != 0 {
		t.Fatalf("validate: %v", errs)
	}
	cfg, err := p.Resolve(0)
	if err != nil {
		t.Fatal(err)
	}
	l := cfg.Limits
	if l.Thresholds.MinFPSMean != 0 || l.Thresholds.MaxJankPct != 0 || l.Thresholds.MaxCPU != 0 ||
		l.Thresholds.MaxMemMB != 0 || l.Thresholds.MaxTempC != 0 || l.MaxFrameP95 != 0 || l.MaxStartupMS != 0 {
		t.Errorf("smoke gates must all be 0: %+v", l)
	}
	if len(cfg.Case.Steps) != 2 || cfg.Case.Steps[0].Action != "launch" || cfg.Case.Steps[1].Seconds != 5 {
		t.Errorf("steps = %+v", cfg.Case.Steps)
	}
}

func TestEffectivePlanRequiresPackage(t *testing.T) {
	if _, err := effectivePlan(Options{}); err == nil || !strings.Contains(err.Error(), "package") {
		t.Errorf("err = %v", err)
	}
	p, err := effectivePlan(Options{Package: "com.example"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Cases[0].Package != "com.example" {
		t.Errorf("package = %q", p.Cases[0].Package)
	}
}

func TestWriteCSV(t *testing.T) {
	rows := []Row{
		{DeviceInfo: DeviceInfo{Serial: "s1", Model: "Pixel", Android: "15", SDK: "35", ABI: "arm64-v8a", Resolution: "1080x2400"},
			Case: "smoke", Package: "com.a", Installed: true, StartupMS: 1200, FPSMean: 30, JankPct: 1, Failed: false},
		{DeviceInfo: DeviceInfo{Serial: "s2"}, Case: "smoke", Package: "com.a", Installed: false, Failed: true, Error: "安装失败", Crash: true},
	}
	var buf bytes.Buffer
	if err := WriteCSV(&buf, rows); err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("records = %d", len(recs))
	}
	if recs[0][0] != "serial" || recs[0][len(recs[0])-1] != "artifacts" {
		t.Errorf("header = %v", recs[0])
	}
	if recs[1][0] != "s1" || recs[1][8] != "true" || recs[2][21] != "false" {
		t.Errorf("row = %v / %v", recs[1], recs[2])
	}
	// Columns: installed=8, install_skipped=9, passed=21.
	if recs[1][21] != "true" {
		t.Errorf("passed = %q", recs[1][21])
	}
}

func TestWriteMatrix(t *testing.T) {
	root := t.TempDir()
	m := Matrix{Root: root, Devices: 1, Cases: 1, Rows: []Row{{
		DeviceInfo: DeviceInfo{Serial: "s1", Model: "Pixel"}, Case: "smoke", Package: "com.a",
	}}}
	writeMatrix(&m)
	if m.CSV == "" || m.JSON == "" {
		t.Fatalf("paths = %+v", m)
	}
	if _, err := os.Stat(m.CSV); err != nil {
		t.Error(err)
	}
	b, err := os.ReadFile(m.JSON)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Matrix
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Rows[0].Serial != "s1" {
		t.Errorf("json = %+v", decoded)
	}
	if !strings.HasSuffix(m.CSV, "compat-matrix.csv") {
		t.Errorf("csv = %q", m.CSV)
	}
	if filepath.Dir(m.JSON) != root {
		t.Errorf("json dir = %q", filepath.Dir(m.JSON))
	}
}

func TestSafeName(t *testing.T) {
	if got := safe("127.0.0.1:16384"); got != "127.0.0.1-16384" {
		t.Errorf("safe = %q", got)
	}
	if got := safe("  "); got != "device" {
		t.Errorf("empty = %q", got)
	}
	if got := safe(strings.Repeat("a", 100)); len(got) != 60 {
		t.Errorf("len = %d", len(got))
	}
}
