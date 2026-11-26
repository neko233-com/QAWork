// Package automation turns a qawork.yaml plan into a repeatable Android
// verification run: launch the app, drive it with adb input, sample
// performance the whole time, gate the result, and keep CSV/JSON/screenshots
// as evidence. It shares perf's sampler and CSV, so an automated run and a
// hand-run measure the same thing.
package automation

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/neko233-com/QAWork/internal/perf"
)

// Plan is the qawork.yaml document.
type Plan struct {
	Version  int      `yaml:"version" json:"version"`
	Defaults Defaults `yaml:"defaults" json:"defaults"`
	Cases    []Case   `yaml:"cases" json:"cases"`
}

// Defaults apply to every case and can be overridden per case.
type Defaults struct {
	Serial    string `yaml:"serial" json:"serial,omitempty"`
	Interval  string `yaml:"interval" json:"interval,omitempty"`
	Warmup    string `yaml:"warmup" json:"warmup,omitempty"`
	Settle    string `yaml:"settle" json:"settle,omitempty"`
	Artifacts string `yaml:"artifacts" json:"artifacts,omitempty"`
	Assert    Assert `yaml:"assert" json:"assert,omitempty"`
}

// Case is one scenario: one target, a list of steps, and its limits.
type Case struct {
	Name      string `yaml:"name" json:"name"`
	Serial    string `yaml:"serial" json:"serial,omitempty"`
	Package   string `yaml:"package" json:"package"`
	Activity  string `yaml:"activity" json:"activity,omitempty"`
	Interval  string `yaml:"interval" json:"interval,omitempty"`
	Warmup    string `yaml:"warmup" json:"warmup,omitempty"`
	Settle    string `yaml:"settle" json:"settle,omitempty"`
	Artifacts string `yaml:"artifacts" json:"artifacts,omitempty"`
	Steps     []Step `yaml:"steps" json:"steps"`
	Assert    Assert `yaml:"assert" json:"assert"`
}

// Step is one adb action. Only the fields the action needs are read.
type Step struct {
	Action     string  `yaml:"action" json:"action"`
	X          int     `yaml:"x" json:"x,omitempty"`
	Y          int     `yaml:"y" json:"y,omitempty"`
	X2         int     `yaml:"x2" json:"x2,omitempty"`
	Y2         int     `yaml:"y2" json:"y2,omitempty"`
	DurationMS int     `yaml:"duration_ms" json:"duration_ms,omitempty"`
	Seconds    float64 `yaml:"seconds" json:"seconds,omitempty"`
	MS         int     `yaml:"ms" json:"ms,omitempty"`
	Key        string  `yaml:"key" json:"key,omitempty"`
	Text       string  `yaml:"text" json:"text,omitempty"`
	Cold       *bool   `yaml:"cold" json:"cold,omitempty"`
}

// Assert overrides limits. A nil field means "use the default"; an explicit
// 0 disables that check.
type Assert struct {
	MinFPS        *float64 `yaml:"min_fps" json:"min_fps,omitempty"`
	MaxJank       *float64 `yaml:"max_jank" json:"max_jank,omitempty"`
	MaxCPU        *float64 `yaml:"max_cpu" json:"max_cpu,omitempty"`
	MaxMem        *float64 `yaml:"max_mem" json:"max_mem,omitempty"`
	MaxTemp       *float64 `yaml:"max_temp_c" json:"max_temp_c,omitempty"`
	MaxFrameP95MS *float64 `yaml:"max_frame_p95_ms" json:"max_frame_p95_ms,omitempty"`
	MaxStartupMS  *float64 `yaml:"max_startup_ms" json:"max_startup_ms,omitempty"`
}

// Limits are the resolved thresholds of one case.
type Limits struct {
	Thresholds   perf.Thresholds `json:"thresholds"`
	MaxFrameP95  float64         `json:"max_frame_p95_ms"`
	MaxStartupMS float64         `json:"max_startup_ms"`
}

// Config is a case with defaults applied.
type Config struct {
	Case     Case          `json:"case"`
	Serial   string        `json:"serial"`
	Interval time.Duration `json:"interval"`
	Warmup   time.Duration `json:"warmup"`
	Settle   time.Duration `json:"settle"`
	Limits   Limits        `json:"limits"`
}

// DefaultLimits are the starting gates, same spirit as the PerfDog mode.
func DefaultLimits() Limits {
	return Limits{
		Thresholds:   perf.DefaultThresholds(),
		MaxFrameP95:  33,
		MaxStartupMS: 5000,
	}
}

// LoadPlan reads a qawork.yaml.
func LoadPlan(path string) (*Plan, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParsePlan(b)
}

// ParsePlan parses and validates a plan.
func ParsePlan(b []byte) (*Plan, error) {
	var p Plan
	if err := yaml.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("解析 qawork.yaml: %w", err)
	}
	if p.Version == 0 {
		p.Version = 1
	}
	if errs := p.Validate(); len(errs) > 0 {
		return &p, &ValidationError{Errors: errs}
	}
	return &p, nil
}

// ValidationError collects every problem so one run fixes them all.
type ValidationError struct {
	Errors []error
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Errors))
	for _, err := range e.Errors {
		parts = append(parts, err.Error())
	}
	return strings.Join(parts, "; ")
}

// Validate checks the whole plan without touching a device.
func (p *Plan) Validate() []error {
	var errs []error
	if p.Version != 1 {
		errs = append(errs, fmt.Errorf("version 只支持 1，当前 %d", p.Version))
	}
	if _, err := durationOf(p.Defaults.Interval, time.Second); err != nil {
		errs = append(errs, fmt.Errorf("defaults.interval: %w", err))
	} else if d, _ := durationOf(p.Defaults.Interval, time.Second); d < 200*time.Millisecond || d > 10*time.Second {
		errs = append(errs, fmt.Errorf("defaults.interval 需在 200ms–10s"))
	}
	if _, err := durationOf(p.Defaults.Warmup, time.Second); err != nil {
		errs = append(errs, fmt.Errorf("defaults.warmup: %w", err))
	} else if d, _ := durationOf(p.Defaults.Warmup, time.Second); d < 0 || d > time.Minute {
		errs = append(errs, fmt.Errorf("defaults.warmup 需在 0–60s"))
	}
	if _, err := durationOf(p.Defaults.Settle, time.Second); err != nil {
		errs = append(errs, fmt.Errorf("defaults.settle: %w", err))
	} else if d, _ := durationOf(p.Defaults.Settle, time.Second); d < 0 || d > time.Minute {
		errs = append(errs, fmt.Errorf("defaults.settle 需在 0–60s"))
	}
	if len(p.Cases) == 0 {
		errs = append(errs, fmt.Errorf("至少需要一个 case"))
	}
	names := map[string]bool{}
	for i, c := range p.Cases {
		prefix := fmt.Sprintf("cases[%d]", i)
		if c.Name == "" {
			errs = append(errs, fmt.Errorf("%s.name 不能为空", prefix))
		} else if names[c.Name] {
			errs = append(errs, fmt.Errorf("%s.name %q 重复", prefix, c.Name))
		} else {
			names[c.Name] = true
		}
		if c.Package == "" {
			errs = append(errs, fmt.Errorf("%s.package 不能为空", prefix))
		}
		interval, err := durationOf(c.Interval, time.Second)
		if err != nil || interval < 200*time.Millisecond || interval > 10*time.Second {
			errs = append(errs, fmt.Errorf("%s.interval 需在 200ms–10s", prefix))
		}
		warmup, err := durationOf(c.Warmup, time.Second)
		if err != nil || warmup < 0 || warmup > time.Minute {
			errs = append(errs, fmt.Errorf("%s.warmup 需在 0–60s", prefix))
		}
		settle, err := durationOf(c.Settle, time.Second)
		if err != nil || settle < 0 || settle > time.Minute {
			errs = append(errs, fmt.Errorf("%s.settle 需在 0–60s", prefix))
		}
		errs = append(errs, validateSteps(prefix, c.Steps)...)
		errs = append(errs, validateAssert(prefix, c.Assert)...)
	}
	errs = append(errs, validateAssert("defaults.assert", p.Defaults.Assert)...)
	return errs
}

func validateSteps(prefix string, steps []Step) []error {
	var errs []error
	if len(steps) > 100 {
		errs = append(errs, fmt.Errorf("%s.steps 最多 100 步", prefix))
	}
	for i, s := range steps {
		where := fmt.Sprintf("%s.steps[%d]", prefix, i)
		switch s.Action {
		case "launch":
			if i != 0 {
				errs = append(errs, fmt.Errorf("%s.launch 只能是第一步（采样器启动后强退会让 PID 失效）", where))
			}
		case "wait":
			if s.Seconds <= 0 && s.MS <= 0 {
				errs = append(errs, fmt.Errorf("%s.wait 需要 seconds 或 ms", where))
			}
			if s.Seconds > 120 || s.MS > 120000 {
				errs = append(errs, fmt.Errorf("%s.wait 最多 120s", where))
			}
		case "tap":
			if s.X < 0 || s.Y < 0 {
				errs = append(errs, fmt.Errorf("%s.tap 需要非负 x/y", where))
			}
		case "swipe":
			if s.X < 0 || s.Y < 0 || s.X2 < 0 || s.Y2 < 0 {
				errs = append(errs, fmt.Errorf("%s.swipe 需要非负 x/y/x2/y2", where))
			}
			if s.DurationMS < 0 || s.DurationMS > 5000 {
				errs = append(errs, fmt.Errorf("%s.swipe duration_ms 需在 0–5000", where))
			}
		case "keyevent":
			if strings.TrimSpace(s.Key) == "" {
				errs = append(errs, fmt.Errorf("%s.keyevent 需要 key", where))
			}
		case "text":
			if s.Text == "" {
				errs = append(errs, fmt.Errorf("%s.text 不能为空", where))
			}
			if !isASCII(s.Text) {
				errs = append(errs, fmt.Errorf("%s.text 只支持 ASCII（adb input text 不收中文）", where))
			}
			if len(s.Text) > 300 {
				errs = append(errs, fmt.Errorf("%s.text 最多 300 字符", where))
			}
		case "screenshot":
			// no fields
		default:
			errs = append(errs, fmt.Errorf("%s.action %q 不支持（launch/wait/tap/swipe/keyevent/text/screenshot）", where, s.Action))
		}
	}
	return errs
}

func validateAssert(prefix string, a Assert) []error {
	var errs []error
	fields := map[string]*float64{
		"min_fps": a.MinFPS, "max_jank": a.MaxJank, "max_cpu": a.MaxCPU,
		"max_mem": a.MaxMem, "max_temp_c": a.MaxTemp, "max_frame_p95_ms": a.MaxFrameP95MS,
		"max_startup_ms": a.MaxStartupMS,
	}
	for name, v := range fields {
		if v != nil && *v < 0 {
			errs = append(errs, fmt.Errorf("%s.%s 不能为负", prefix, name))
		}
	}
	return errs
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return false
		}
	}
	return true
}

func durationOf(s string, def time.Duration) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return def, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("时长 %q 无效（用 1s / 500ms）", s)
	}
	if d < 0 {
		return 0, fmt.Errorf("时长不能为负: %q", s)
	}
	return d, nil
}

// Resolve applies defaults to one case and returns its runnable config.
func (p *Plan) Resolve(i int) (Config, error) {
	if i < 0 || i >= len(p.Cases) {
		return Config{}, fmt.Errorf("case 下标越界: %d", i)
	}
	c := p.Cases[i]
	serial := c.Serial
	if serial == "" {
		serial = p.Defaults.Serial
	}
	interval, err := durationOf(c.Interval, 0)
	if err != nil {
		return Config{}, err
	}
	if interval == 0 && c.Interval == "" {
		interval, err = durationOf(p.Defaults.Interval, time.Second)
	}
	if err != nil {
		return Config{}, err
	}
	warmup, err := durationOf(c.Warmup, 0)
	if err != nil {
		return Config{}, err
	}
	if warmup == 0 && c.Warmup == "" {
		warmup, err = durationOf(p.Defaults.Warmup, time.Second)
	}
	if err != nil {
		return Config{}, err
	}
	settle, err := durationOf(c.Settle, 0)
	if err != nil {
		return Config{}, err
	}
	if settle == 0 && c.Settle == "" {
		settle, err = durationOf(p.Defaults.Settle, time.Second)
	}
	if err != nil {
		return Config{}, err
	}
	limits := resolveLimits(p.Defaults.Assert, c.Assert)
	cfg := Config{Case: c, Serial: serial, Interval: interval, Warmup: warmup, Settle: settle, Limits: limits}
	if cfg.Warmup < 0 || cfg.Settle < 0 {
		return Config{}, fmt.Errorf("warmup/settle 不能为负")
	}
	if cfg.Interval < 200*time.Millisecond {
		cfg.Interval = 200 * time.Millisecond
	}
	return cfg, nil
}

func resolveLimits(defaults, overrides Assert) Limits {
	l := DefaultLimits()
	apply := func(a Assert) {
		if a.MinFPS != nil {
			l.Thresholds.MinFPSMean = *a.MinFPS
		}
		if a.MaxJank != nil {
			l.Thresholds.MaxJankPct = *a.MaxJank
		}
		if a.MaxCPU != nil {
			l.Thresholds.MaxCPU = *a.MaxCPU
		}
		if a.MaxMem != nil {
			l.Thresholds.MaxMemMB = *a.MaxMem
		}
		if a.MaxTemp != nil {
			l.Thresholds.MaxTempC = *a.MaxTemp
		}
		if a.MaxFrameP95MS != nil {
			l.MaxFrameP95 = *a.MaxFrameP95MS
		}
		if a.MaxStartupMS != nil {
			l.MaxStartupMS = *a.MaxStartupMS
		}
	}
	apply(defaults)
	apply(overrides)
	return l
}

// Names lists the case names in plan order.
func (p *Plan) Names() []string {
	names := make([]string, 0, len(p.Cases))
	for _, c := range p.Cases {
		names = append(names, c.Name)
	}
	return names
}
