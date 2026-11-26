package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neko233-com/QAWork/internal/perf"
	"github.com/neko233-com/QAWork/internal/setup"
)

func newTestServer() *Server {
	s := NewServer()
	s.Tools = append(s.Tools, Tool{
		Name:   "fake_ok",
		Schema: map[string]any{"type": "object"},
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			return map[string]any{"echo": args["x"]}, nil
		},
	}, Tool{
		Name:   "fake_fail",
		Schema: map[string]any{"type": "object"},
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			return nil, context.DeadlineExceeded
		},
	})
	return s
}

func handleJSON(t *testing.T, s *Server, msg string) (map[string]any, bool) {
	t.Helper()
	resp, notify, err := s.Handle(context.Background(), []byte(msg))
	if err != nil {
		t.Fatal(err)
	}
	if notify {
		return nil, true
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m, false
}

func TestInitialize(t *testing.T) {
	s := NewServer()
	m, _ := handleJSON(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	res, ok := m["result"].(map[string]any)
	if !ok {
		t.Fatalf("result = %v", m)
	}
	if res["protocolVersion"] != "2025-06-18" {
		t.Errorf("protocol = %v", res["protocolVersion"])
	}
	info := res["serverInfo"].(map[string]any)
	if info["name"] != "qawork" || info["version"] == "" {
		t.Errorf("serverInfo = %v", info)
	}
	if res["instructions"] == "" {
		t.Error("instructions empty")
	}
}

func TestInitializeDefaultVersion(t *testing.T) {
	m, _ := handleJSON(t, NewServer(), `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	got := m["result"].(map[string]any)["protocolVersion"]
	if got != "2025-06-18" {
		t.Errorf("version = %v", got)
	}
}

func TestNotificationGetsNoAnswer(t *testing.T) {
	_, notify := handleJSON(t, NewServer(), `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if !notify {
		t.Fatal("want notification")
	}
}

func TestPing(t *testing.T) {
	m, _ := handleJSON(t, NewServer(), `{"jsonrpc":"2.0","id":9,"method":"ping"}`)
	if _, ok := m["result"].(map[string]any); !ok {
		t.Errorf("ping result = %v", m)
	}
}

func TestToolsList(t *testing.T) {
	m, _ := handleJSON(t, NewServer(), `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	result := m["result"].(map[string]any)
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) < 7 {
		t.Fatalf("tools = %v", result)
	}
	names := map[string]bool{}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		names[tool["name"].(string)] = true
		if tool["inputSchema"] == nil {
			t.Errorf("tool %v has no schema", tool["name"])
		}
	}
	for _, want := range []string{
		"qawork_devices", "qawork_perf", "qawork_report", "qawork_doctor",
		"qawork_validate_plan", "qawork_run_plan", "qawork_compat_matrix",
	} {
		if !names[want] {
			t.Errorf("missing tool %s", want)
		}
	}
}

func TestUnknownMethod(t *testing.T) {
	m, _ := handleJSON(t, NewServer(), `{"jsonrpc":"2.0","id":3,"method":"resources/list"}`)
	e, ok := m["error"].(map[string]any)
	if !ok || e["code"].(float64) != -32601 {
		t.Errorf("error = %v", m)
	}
}

func TestToolCallOK(t *testing.T) {
	m, _ := handleJSON(t, newTestServer(), `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"fake_ok","arguments":{"x":"y"}}}`)
	res := m["result"].(map[string]any)
	if res["isError"] != false {
		t.Errorf("isError = %v", res["isError"])
	}
	if res["structuredContent"].(map[string]any)["echo"] != "y" {
		t.Errorf("structured = %v", res["structuredContent"])
	}
	content := res["content"].([]any)
	if len(content) == 0 || content[0].(map[string]any)["type"] != "text" {
		t.Errorf("content = %v", content)
	}
}

func TestToolCallFailure(t *testing.T) {
	m, _ := handleJSON(t, newTestServer(), `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"fake_fail"}}`)
	res := m["result"].(map[string]any)
	if res["isError"] != true {
		t.Errorf("isError = %v", res["isError"])
	}
}

func TestToolCallUnknownTool(t *testing.T) {
	m, _ := handleJSON(t, newTestServer(), `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"nope"}}`)
	res := m["result"].(map[string]any)
	if res["isError"] != true {
		t.Errorf("isError = %v", res["isError"])
	}
	if !strings.Contains(res["content"].([]any)[0].(map[string]any)["text"].(string), "未知工具") {
		t.Errorf("content = %v", res["content"])
	}
}

func TestToolCallBadArguments(t *testing.T) {
	m, _ := handleJSON(t, newTestServer(), `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"fake_ok","arguments":[1]}}`)
	res := m["result"].(map[string]any)
	if res["isError"] != true {
		t.Errorf("isError = %v", res["isError"])
	}
}

func TestMalformedJSON(t *testing.T) {
	m, _ := handleJSON(t, NewServer(), `{`)
	e, ok := m["error"].(map[string]any)
	if !ok || e["code"].(float64) != -32700 {
		t.Errorf("error = %v", m)
	}
}

func TestMissingMethod(t *testing.T) {
	m, _ := handleJSON(t, NewServer(), `{"jsonrpc":"2.0","id":8}`)
	e := m["error"].(map[string]any)
	if e["code"].(float64) != -32600 {
		t.Errorf("error = %v", e)
	}
}

func TestServeLoop(t *testing.T) {
	s := newTestServer()
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" +
			`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n")
	var out bytes.Buffer
	if err := s.Serve(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("responses = %d: %q", len(lines), out.String())
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first["id"].(float64) != 1 {
		t.Errorf("first = %v", first)
	}
}

func TestDefaultReportTool(t *testing.T) {
	s := NewServer()
	var report *Tool
	for i := range s.Tools {
		if s.Tools[i].Name == "qawork_report" {
			report = &s.Tools[i]
		}
	}
	if report == nil {
		t.Fatal("report tool missing")
	}
	csvPath := filepath.Join(t.TempDir(), "run.csv")
	samples := []perf.Sample{
		{At: time.Now(), FPS: 55, FrameMS: 18.2, CPU: 30, MemMB: 200, TempC: 36, Threads: 20},
		{At: time.Now().Add(time.Second), FPS: 45, FrameMS: 22.2, CPU: 40, MemMB: 210, TempC: 37, Threads: 20},
	}
	if err := perf.SaveCSV(csvPath, samples); err != nil {
		t.Fatal(err)
	}
	result, err := report.Run(context.Background(), map[string]any{
		"csv": csvPath,
		"thresholds": map[string]any{
			"min_fps": 55.0, "max_jank": 0.0, "max_cpu": 0.0, "max_mem": 0.0, "max_temp_c": 0.0,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := result.(map[string]any)
	if m["failed"] != true {
		t.Errorf("failed = %v", m["failed"])
	}
	checks := m["checks"].([]perf.Check)
	if len(checks) != 1 || checks[0].Name != "平均帧率" {
		t.Errorf("checks = %+v", checks)
	}
}

func TestPerfToolRequiresPackage(t *testing.T) {
	s := NewServer()
	var tool *Tool
	for i := range s.Tools {
		if s.Tools[i].Name == "qawork_perf" {
			tool = &s.Tools[i]
		}
	}
	if _, err := tool.Run(context.Background(), map[string]any{}); err == nil {
		t.Fatal("want error")
	}
}

func TestThresholdsOf(t *testing.T) {
	tk := thresholdsOf(map[string]any{"thresholds": map[string]any{
		"min_fps": 60.0, "max_jank": 3.0, "max_cpu": 50.0, "max_mem": 512.0, "max_temp_c": 40.0,
	}})
	if tk.MinFPSMean != 60 || tk.MaxJankPct != 3 || tk.MaxCPU != 50 || tk.MaxMemMB != 512 || tk.MaxTempC != 40 {
		t.Errorf("thresholds = %+v", tk)
	}
	if def := thresholdsOf(map[string]any{}); def != perf.DefaultThresholds() {
		t.Errorf("default = %+v", def)
	}
}

func TestDoctorWithoutADB(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QAWORK_HOME", home)
	t.Setenv("QAWORK_ADB", "")
	t.Setenv("PATH", "")
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Setenv("ANDROID_HOME", "")
	t.Setenv("ANDROID_SDK_ROOT", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	result, err := toolDoctor(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	m := result.(map[string]any)
	if m["adb_usable"] != false {
		t.Errorf("adb_usable = %v", m["adb_usable"])
	}
	next, _ := m["next"].(string)
	if !strings.Contains(next, "adb") {
		t.Errorf("next = %q", next)
	}
}

func TestNextStepReady(t *testing.T) {
	step := nextStep([]setup.Dep{{ID: "adb", Required: true, Status: setup.StatusOK}})
	if !strings.Contains(step, "环境就绪") {
		t.Errorf("step = %q", step)
	}
}

func findTool(t *testing.T, name string) *Tool {
	t.Helper()
	s := NewServer()
	for i := range s.Tools {
		if s.Tools[i].Name == name {
			return &s.Tools[i]
		}
	}
	t.Fatalf("tool %s missing", name)
	return nil
}

func TestValidatePlanTool(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qawork.yaml")
	if err := os.WriteFile(path, []byte("version: 1\ncases:\n  - name: smoke\n    package: com.a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := findTool(t, "qawork_validate_plan")
	result, err := tool.Run(context.Background(), map[string]any{"plan": path})
	if err != nil {
		t.Fatal(err)
	}
	m := result.(map[string]any)
	if m["valid"] != true {
		t.Errorf("result = %+v", m)
	}
	cases := m["cases"].([]string)
	if len(cases) != 1 || cases[0] != "smoke" {
		t.Errorf("cases = %v", cases)
	}
}

func TestValidatePlanToolReportsErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(path, []byte("version: 2\ncases: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := findTool(t, "qawork_validate_plan")
	result, err := tool.Run(context.Background(), map[string]any{"plan": path})
	if err != nil {
		t.Fatal(err)
	}
	m := result.(map[string]any)
	if m["valid"] != false {
		t.Errorf("result = %+v", m)
	}
	if errs, _ := m["errors"].([]string); len(errs) == 0 {
		t.Errorf("errors = %v", m["errors"])
	}
}

func TestCompatToolRequiresPackageWithoutPlan(t *testing.T) {
	tool := findTool(t, "qawork_compat_matrix")
	if _, err := tool.Run(context.Background(), map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "package") {
		t.Errorf("err = %v", err)
	}
}

func TestArgStringSlice(t *testing.T) {
	args := map[string]any{"devices": []any{"a", " b ", 3}}
	if got := argStringSlice(args, "devices"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("slice = %v", got)
	}
	args = map[string]any{"devices": "a, b ,"}
	if got := argStringSlice(args, "devices"); len(got) != 2 {
		t.Errorf("string = %v", got)
	}
	if got := argStringSlice(map[string]any{}, "devices"); got != nil {
		t.Errorf("missing = %v", got)
	}
}
