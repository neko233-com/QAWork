// Package adbkit is the explicit, testable slice of adb that QAWork uses
// outside the perf sampler: device facts, app lifecycle, logcat, screenshots,
// permissions and a few safe input helpers. There is deliberately no
// arbitrary shell passthrough.
package adbkit

import (
	"context"
	"encoding/xml"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/neko233-com/QAWork/internal/perf"
)

// Sheller is anything that can run a device shell command. automation.Device
// satisfies it, so the UI-tree helpers work in both CLI and automation tests.
type Sheller interface {
	Shell(ctx context.Context, command string) (string, error)
}

// Client is the adb-backed implementation of the device helpers.
type Client struct {
	ADB perf.ADB
}

// DeviceInfo is the static fact sheet of one device.
type DeviceInfo struct {
	Serial       string  `json:"serial"`
	Model        string  `json:"model,omitempty"`
	Brand        string  `json:"brand,omitempty"`
	Android      string  `json:"android,omitempty"`
	SDK          string  `json:"sdk,omitempty"`
	ABI          string  `json:"abi,omitempty"`
	Resolution   string  `json:"resolution,omitempty"`
	Density      string  `json:"density,omitempty"`
	BatteryLevel int     `json:"battery_level,omitempty"`
	BatteryTempC float64 `json:"battery_temp_c,omitempty"`
}

// Info collects the device facts used by the matrix and the CLI.
func (c Client) Info(ctx context.Context, serial string) (DeviceInfo, error) {
	info := DeviceInfo{Serial: serial}
	props, err := c.ADB.Shell(ctx, serial, "getprop")
	if err != nil {
		return info, fmt.Errorf("getprop: %w", err)
	}
	m := ParseGetprop(props)
	info.Model = m["ro.product.model"]
	info.Brand = m["ro.product.brand"]
	info.Android = m["ro.build.version.release"]
	info.SDK = m["ro.build.version.sdk"]
	info.ABI = m["ro.product.cpu.abi"]
	info.Resolution = ParseResolution(shell(ctx, c.ADB, serial, "wm size"))
	info.Density = ParseDensity(shell(ctx, c.ADB, serial, "wm density"))
	level, temp := ParseBattery(shell(ctx, c.ADB, serial, "dumpsys battery"))
	info.BatteryLevel = level
	info.BatteryTempC = temp
	return info, nil
}

func shell(ctx context.Context, adb perf.ADB, serial, cmd string) string {
	out, err := adb.Shell(ctx, serial, cmd)
	if err != nil {
		return ""
	}
	return out
}

// ParseGetprop reads the "[key]: [value]" dump.
func ParseGetprop(out string) map[string]string {
	props := map[string]string{}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "[") {
			continue
		}
		keyEnd := strings.Index(line, "]: [")
		if keyEnd < 0 {
			continue
		}
		key := line[1:keyEnd]
		value := line[keyEnd+4:]
		value = strings.TrimSuffix(value, "]")
		props[key] = strings.TrimSpace(value)
	}
	return props
}

// ParseResolution prefers the override size, like adb wm size.
func ParseResolution(out string) string {
	physical, override := "", ""
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "Physical size:"):
			physical = strings.TrimSpace(strings.TrimPrefix(line, "Physical size:"))
		case strings.HasPrefix(line, "Override size:"):
			override = strings.TrimSpace(strings.TrimPrefix(line, "Override size:"))
		}
	}
	if override != "" {
		return override
	}
	return physical
}

// ParseDensity prefers the override density.
func ParseDensity(out string) string {
	physical, override := "", ""
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "Physical density:"):
			physical = strings.TrimSpace(strings.TrimPrefix(line, "Physical density:"))
		case strings.HasPrefix(line, "Override density:"):
			override = strings.TrimSpace(strings.TrimPrefix(line, "Override density:"))
		}
	}
	if override != "" {
		return override
	}
	return physical
}

// ParseBattery reads level and temperature from dumpsys battery. Android
// reports temperature in tenths of a degree Celsius.
func ParseBattery(out string) (level int, tempC float64) {
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "level:"):
			level, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "level:")))
		case strings.HasPrefix(line, "temperature:"):
			v, _ := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, "temperature:")), 64)
			if v != 0 {
				tempC = v / 10
			}
		}
	}
	return level, tempC
}

// Install installs an APK. replace is adb install -r; grant is -g.
func (c Client) Install(ctx context.Context, serial, apk string, replace, grant bool) (string, error) {
	if strings.TrimSpace(apk) == "" {
		return "", fmt.Errorf("APK 路径为空")
	}
	args := []string{"-s", serial, "install"}
	if replace {
		args = append(args, "-r")
	}
	if grant {
		args = append(args, "-g")
	}
	args = append(args, apk)
	out, err := c.ADB.Exec(ctx, args...)
	return strings.TrimSpace(out), err
}

// Uninstall removes a package.
func (c Client) Uninstall(ctx context.Context, serial, pkg string, keepData bool) (string, error) {
	if !ValidPackage(pkg) {
		return "", fmt.Errorf("包名无效: %q", pkg)
	}
	args := []string{"-s", serial, "uninstall"}
	if keepData {
		args = append(args, "-k")
	}
	args = append(args, pkg)
	out, err := c.ADB.Exec(ctx, args...)
	return strings.TrimSpace(out), err
}

// Stop force-stops the app.
func (c Client) Stop(ctx context.Context, serial, pkg string) (string, error) {
	if !ValidPackage(pkg) {
		return "", fmt.Errorf("包名无效: %q", pkg)
	}
	return c.ADB.Shell(ctx, serial, "am force-stop "+pkg)
}

// Clear wipes the app data. Callers must check for "Success".
func (c Client) Clear(ctx context.Context, serial, pkg string) (string, error) {
	if !ValidPackage(pkg) {
		return "", fmt.Errorf("包名无效: %q", pkg)
	}
	return c.ADB.Shell(ctx, serial, "pm clear "+pkg)
}

// Grant grants runtime permissions one by one.
func (c Client) Grant(ctx context.Context, serial, pkg string, permissions []string) ([]string, error) {
	return c.perms(ctx, serial, pkg, permissions, "grant")
}

// Revoke revokes runtime permissions one by one.
func (c Client) Revoke(ctx context.Context, serial, pkg string, permissions []string) ([]string, error) {
	return c.perms(ctx, serial, pkg, permissions, "revoke")
}

func (c Client) perms(ctx context.Context, serial, pkg string, permissions []string, verb string) ([]string, error) {
	if !ValidPackage(pkg) {
		return nil, fmt.Errorf("包名无效: %q", pkg)
	}
	if len(permissions) == 0 {
		return nil, fmt.Errorf("permissions 为空")
	}
	var out []string
	for _, p := range permissions {
		p = strings.TrimSpace(p)
		if !ValidPermission(p) {
			return out, fmt.Errorf("权限名无效: %q", p)
		}
		o, err := c.ADB.Shell(ctx, serial, "pm "+verb+" "+pkg+" "+p)
		if err != nil {
			return out, fmt.Errorf("%s %s: %w", verb, p, err)
		}
		out = append(out, strings.TrimSpace(o))
	}
	return out, nil
}

// Screenshot captures one PNG frame.
func (c Client) Screenshot(ctx context.Context, serial string) ([]byte, error) {
	return c.ADB.ExecBytes(ctx, "-s", serial, "exec-out", "screencap", "-p")
}

// LogcatDump returns the last n lines without following the stream.
func (c Client) LogcatDump(ctx context.Context, serial string, lines int, clearFirst bool) (string, error) {
	if lines <= 0 {
		lines = 2000
	}
	if lines > 20000 {
		lines = 20000
	}
	if clearFirst {
		if _, err := c.ADB.Shell(ctx, serial, "logcat -c"); err != nil {
			return "", fmt.Errorf("logcat -c: %w", err)
		}
	}
	return c.ADB.Shell(ctx, serial, fmt.Sprintf("logcat -d -v brief -t %d", lines))
}

// Monkey runs a bounded random-input stress loop and returns adb output.
func (c Client) Monkey(ctx context.Context, serial, pkg string, events, throttleMS int) (string, error) {
	if !ValidPackage(pkg) {
		return "", fmt.Errorf("包名无效: %q", pkg)
	}
	if events <= 0 {
		events = 1000
	}
	if events > 1000000 {
		events = 1000000
	}
	if throttleMS < 0 {
		throttleMS = 0
	}
	if throttleMS > 5000 {
		throttleMS = 5000
	}
	return c.ADB.Shell(ctx, serial, fmt.Sprintf("monkey -p %s --throttle %d -v %d", pkg, throttleMS, events))
}

// Animations turns system animations off/on; off makes UI assertions and
// screenshots less flaky, but it is a device setting the caller should restore.
func (c Client) Animations(ctx context.Context, serial string, on bool) error {
	v := "1"
	if !on {
		v = "0"
	}
	for _, key := range []string{"window_animation_scale", "transition_animation_scale", "animator_duration_scale"} {
		if _, err := c.ADB.Shell(ctx, serial, "settings put global "+key+" "+v); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	return nil
}

// Wake turns the screen on.
func (c Client) Wake(ctx context.Context, serial string) error {
	_, err := c.ADB.Shell(ctx, serial, "input keyevent KEYCODE_WAKEUP")
	return err
}

// Unlock dismisses the keyguard and swipes up once. It cannot defeat a PIN.
func (c Client) Unlock(ctx context.Context, serial string) error {
	for _, cmd := range []string{
		"input keyevent KEYCODE_WAKEUP",
		"wm dismiss-keyguard",
		"input swipe 540 1600 540 600 300",
	} {
		if _, err := c.ADB.Shell(ctx, serial, cmd); err != nil {
			return fmt.Errorf("%s: %w", cmd, err)
		}
	}
	return nil
}

// Push copies a local file to the device.
func (c Client) Push(ctx context.Context, serial, local, remote string) (string, error) {
	if strings.TrimSpace(local) == "" || strings.TrimSpace(remote) == "" {
		return "", fmt.Errorf("push 需要 local 和 remote")
	}
	return c.ADB.Exec(ctx, "-s", serial, "push", local, remote)
}

// Pull copies a device file to the host.
func (c Client) Pull(ctx context.Context, serial, remote, local string) (string, error) {
	if strings.TrimSpace(local) == "" || strings.TrimSpace(remote) == "" {
		return "", fmt.Errorf("pull 需要 remote 和 local")
	}
	return c.ADB.Exec(ctx, "-s", serial, "pull", remote, local)
}

// CurrentActivity returns pkg/activity from the window focus (fallback: the
// resumed activity record). Empty means the parser found no reliable answer.
func CurrentActivity(ctx context.Context, s Sheller) (string, error) {
	out, err := s.Shell(ctx, "dumpsys window windows")
	if err == nil {
		if act := ParseCurrentActivity(out); act != "" {
			return act, nil
		}
	}
	out, err = s.Shell(ctx, "dumpsys activity activities")
	if err != nil {
		return "", fmt.Errorf("读取当前 Activity: %w", err)
	}
	return ParseCurrentActivity(out), nil
}

var componentRE = regexp.MustCompile(`([A-Za-z0-9_.]+/[A-Za-z0-9_.$]+)`)

// ParseCurrentActivity extracts a pkg/activity component from dumpsys output.
func ParseCurrentActivity(out string) string {
	lines := strings.Split(out, "\n")
	for _, marker := range []string{"mCurrentFocus=", "mFocusedApp=", "mResumedActivity:", "topResumedActivity="} {
		for _, line := range lines {
			if !strings.Contains(line, marker) {
				continue
			}
			if act := componentRE.FindString(line); act != "" {
				return act
			}
		}
	}
	return ""
}

// NormalizeComponent expands a leading dot class to pkg.Class.
func NormalizeComponent(s string) string {
	s = strings.TrimSpace(s)
	pkg, cls, ok := strings.Cut(s, "/")
	if !ok || pkg == "" || cls == "" {
		return s
	}
	if strings.HasPrefix(cls, ".") {
		cls = pkg + cls
	}
	return pkg + "/" + cls
}

// ActivityMatches compares a dumpsys component with a plan target. A target
// without a slash is treated as a class name or a package.
func ActivityMatches(current, want string) bool {
	current = NormalizeComponent(current)
	want = strings.TrimSpace(want)
	if want == "" {
		return false
	}
	if !strings.Contains(want, "/") {
		curPkg, _, _ := strings.Cut(current, "/")
		if curPkg == want || strings.HasPrefix(curPkg, want+".") {
			return true
		}
		return strings.HasSuffix(current, "/"+want) ||
			strings.HasSuffix(current, want)
	}
	want = NormalizeComponent(want)
	if current == want {
		return true
	}
	// Some OEMs report a leading "." differently; compare the class tail too.
	curPkg, curCls, ok1 := strings.Cut(current, "/")
	wantPkg, wantCls, ok2 := strings.Cut(want, "/")
	return ok1 && ok2 && curPkg == wantPkg && strings.HasSuffix(curCls, wantCls)
}

// DumpUI dumps the accessibility tree and returns the XML. The first dump is
// compressed to keep large game UIs small; a failed dump retries uncompressed.
func DumpUI(ctx context.Context, s Sheller) (string, error) {
	const remote = "/data/local/tmp/qawork-ui.xml"
	if _, err := s.Shell(ctx, "uiautomator dump --compressed "+remote); err != nil {
		if _, err2 := s.Shell(ctx, "uiautomator dump "+remote); err2 != nil {
			return "", fmt.Errorf("uiautomator dump: %w", err2)
		}
	}
	out, err := s.Shell(ctx, "cat "+remote)
	if err != nil {
		return "", fmt.Errorf("读取 UI dump: %w", err)
	}
	return out, nil
}

// UITexts returns all non-empty text and content-desc values in the dump.
func UITexts(xmlText string) []string {
	dec := xml.NewDecoder(strings.NewReader(xmlText))
	var texts []string
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		for _, attr := range start.Attr {
			if attr.Name.Local != "text" && attr.Name.Local != "content-desc" {
				continue
			}
			if v := strings.TrimSpace(attr.Value); v != "" {
				texts = append(texts, v)
			}
		}
	}
	return texts
}

// UIHasText reports whether any visible node contains want.
func UIHasText(xmlText, want string) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return false
	}
	for _, t := range UITexts(xmlText) {
		if strings.Contains(t, want) {
			return true
		}
	}
	return false
}

// ValidPackage accepts the package names adb accepts; this is also the guard
// that keeps a YAML value out of the shell as a raw string.
func ValidPackage(pkg string) bool {
	if pkg == "" || len(pkg) > 255 {
		return false
	}
	for _, r := range pkg {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_':
		default:
			return false
		}
	}
	return !strings.HasPrefix(pkg, ".") && !strings.HasSuffix(pkg, ".")
}

// ValidPermission accepts android.permission.* and custom runtime permissions.
func ValidPermission(p string) bool {
	if p == "" || len(p) > 255 {
		return false
	}
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_':
		default:
			return false
		}
	}
	return strings.Contains(p, ".")
}

// SleepCtx is a small helper shared by the CLI and stress runner.
func SleepCtx(ctx context.Context, d time.Duration) error {
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
