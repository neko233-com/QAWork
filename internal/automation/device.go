package automation

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/neko233-com/QAWork/internal/perf"
)

// Device is what a scenario needs from one Android device. Tests use a fake;
// production uses ADBDevice.
type Device interface {
	Serial() string
	Shell(ctx context.Context, command string) (string, error)
	Screenshot(ctx context.Context) ([]byte, error)
}

// ADBDevice is a Device backed by adb.
type ADBDevice struct {
	ADB      perf.ADB
	SerialNo string
}

// NewADBDevice returns a device bound to one serial.
func NewADBDevice(adb perf.ADB, serial string) *ADBDevice {
	return &ADBDevice{ADB: adb, SerialNo: serial}
}

// Serial is the adb serial.
func (d *ADBDevice) Serial() string { return d.SerialNo }

// Shell runs a shell command on the device.
func (d *ADBDevice) Shell(ctx context.Context, command string) (string, error) {
	return d.ADB.Shell(ctx, d.SerialNo, command)
}

// Screenshot captures the screen as PNG.
func (d *ADBDevice) Screenshot(ctx context.Context) ([]byte, error) {
	return d.ADB.ExecBytes(ctx, "-s", d.SerialNo, "exec-out", "screencap", "-p")
}

// LaunchResult is what one launch step learned.
type LaunchResult struct {
	Activity  string
	StartupMS float64
	Cold      bool
	Measured  bool
}

// Launch starts the app and measures cold/warm startup with am start -W.
func Launch(ctx context.Context, d Device, pkg, activity string, cold bool) (LaunchResult, error) {
	res := LaunchResult{Activity: activity, Cold: cold}
	if pkg == "" {
		return res, fmt.Errorf("package 为空")
	}
	if cold {
		if _, err := d.Shell(ctx, "am force-stop "+quoteShell(pkg)); err != nil {
			return res, fmt.Errorf("force-stop: %w", err)
		}
	}
	if res.Activity == "" {
		act, err := ResolveActivity(ctx, d, pkg)
		if err == nil {
			res.Activity = act
		}
	}
	if res.Activity != "" {
		out, err := d.Shell(ctx, "am start -W -n "+quoteShell(res.Activity))
		if err != nil {
			return res, fmt.Errorf("am start: %w", err)
		}
		total, _, ok := ParseStartupMS(out)
		if ok {
			res.StartupMS = total
			res.Measured = true
		}
		return res, nil
	}
	// No launcher activity resolved: still start it, but do not pretend we
	// measured startup.
	if _, err := d.Shell(ctx, "monkey -p "+quoteShell(pkg)+" -c android.intent.category.LAUNCHER 1"); err != nil {
		return res, fmt.Errorf("启动 %s: %w", pkg, err)
	}
	return res, nil
}

// ResolveActivity asks the package manager for the launcher activity.
func ResolveActivity(ctx context.Context, d Device, pkg string) (string, error) {
	out, err := d.Shell(ctx, "cmd package resolve-activity --brief "+quoteShell(pkg))
	if err != nil {
		return "", err
	}
	return ParseLauncherActivity(out), nil
}

// ParseLauncherActivity reads the component from resolve-activity output.
func ParseLauncherActivity(out string) string {
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "priority=") || strings.HasPrefix(line, "No ") {
			continue
		}
		if strings.Contains(line, "/") {
			return line
		}
	}
	return ""
}

// ParseStartupMS reads TotalTime/WaitTime from am start -W.
func ParseStartupMS(out string) (total, wait float64, ok bool) {
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "TotalTime:"):
			total, _ = strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, "TotalTime:")), 64)
		case strings.HasPrefix(line, "WaitTime:"):
			wait, _ = strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, "WaitTime:")), 64)
		}
	}
	return total, wait, total > 0
}

// StepCommand builds the adb shell command of one input step. Launch and
// screenshot are not shell commands and return empty here.
func StepCommand(s Step) (string, error) {
	switch s.Action {
	case "tap":
		return fmt.Sprintf("input tap %d %d", s.X, s.Y), nil
	case "swipe":
		dur := s.DurationMS
		if dur <= 0 {
			dur = 300
		}
		return fmt.Sprintf("input swipe %d %d %d %d %d", s.X, s.Y, s.X2, s.Y2, dur), nil
	case "keyevent":
		return "input keyevent " + quoteShell(s.Key), nil
	case "text":
		return "input text " + quoteShell(encodeInputText(s.Text)), nil
	case "wait", "launch", "screenshot":
		return "", nil
	default:
		return "", fmt.Errorf("未知动作 %q", s.Action)
	}
}

// encodeInputText escapes spaces for `adb shell input text`.
func encodeInputText(s string) string {
	return strings.ReplaceAll(s, " ", "%s")
}

func quoteShell(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
