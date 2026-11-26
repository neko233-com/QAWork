// Package setup detects and installs the machine-side dependencies QAWork
// needs to measure Android apps: adb (required), scrcpy (optional), and
// the vendor USB driver (manual, never silently installed).
//
// Everything an install writes stays under ToolsDir and the current user's
// PATH: no admin rights, no system directories.
package setup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/neko233-com/QAWork/internal/version"
)

// Status is how a dependency stands on this machine.
type Status string

const (
	StatusOK      Status = "ok"      // installed and callable
	StatusMissing Status = "missing" // installable, not installed
	StatusManual  Status = "manual"  // cannot be installed by QAWork; needs a human
)

// Dep is one row of the environment panel.
type Dep struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
	Status      Status `json:"status"`
	Version     string `json:"version,omitempty"`
	Path        string `json:"path,omitempty"`
	Installable bool   `json:"installable"`
	Note        string `json:"note,omitempty"`
}

// Report is the whole environment as QAWork sees it.
type Report struct {
	ToolsDir string   `json:"tools_dir"`
	ADBPath  string   `json:"adb_path,omitempty"`
	Deps     []Dep    `json:"deps"`
	Required []string `json:"required"`
	Summary  string   `json:"summary"`
}

// HomeDir is where QAWork keeps its tools and sessions; QAWORK_HOME wins.
func HomeDir() string {
	if dir := os.Getenv("QAWORK_HOME"); dir != "" {
		return dir
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, version.Product)
}

// ToolsDir is where the setup panel installs binaries.
func ToolsDir() string {
	return filepath.Join(HomeDir(), "tools")
}

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// ADBPath is the adb QAWork will use: QAWORK_ADB, the bundled platform-tools,
// PATH, then the usual Android SDK install.
func ADBPath() string {
	if p := os.Getenv("QAWORK_ADB"); p != "" && fileExists(p) {
		return p
	}
	if p := filepath.Join(ToolsDir(), "platform-tools", exe("adb")); fileExists(p) {
		return p
	}
	if p, err := exec.LookPath("adb"); err == nil {
		return p
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		p := filepath.Join(local, "Android", "Sdk", "platform-tools", exe("adb"))
		if fileExists(p) {
			return p
		}
	}
	return ""
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// Inspect reads the environment: versions, paths, and what is missing.
func Inspect(ctx context.Context) Report {
	report := Report{ToolsDir: ToolsDir(), Required: []string{"adb"}}
	if p := ADBPath(); p != "" {
		report.ADBPath = p
		report.Deps = append(report.Deps, Dep{
			ID: "adb", Name: "adb（Android Platform Tools）", Required: true,
			Description: "连接 Android 设备、读帧率/CPU/内存。性能模式没有它什么都采不到。",
			Status:      StatusOK,
			Version:     probeVersion(ctx, p, "version"),
			Path:        p,
			Installable: true,
			Note:        adbPathNote(p),
		})
	} else {
		report.Deps = append(report.Deps, Dep{
			ID: "adb", Name: "adb（Android Platform Tools）", Required: true,
			Description: "连接 Android 设备、读帧率/CPU/内存。性能模式没有它什么都采不到。",
			Status:      StatusMissing,
			Installable: true,
			Note:        "必装：下载 Google platform-tools，装到 " + filepath.Join(ToolsDir(), "platform-tools") + " 并写入当前用户 PATH",
		})
	}

	if p := ScrcpyPath(); p != "" {
		report.Deps = append(report.Deps, Dep{
			ID: "scrcpy", Name: "scrcpy", Required: false,
			Description: "USB 投屏、录屏和点按回放；做自动化用例和人工复现时省事。",
			Status:      StatusOK,
			Version:     probeVersion(ctx, p, "--version"),
			Path:        p,
			Installable: true,
			Note:        "依赖 adb；只影响调试便利性，不影响采样口径。",
		})
	} else {
		report.Deps = append(report.Deps, Dep{
			ID: "scrcpy", Name: "scrcpy", Required: false,
			Description: "USB 投屏、录屏和点按回放；做自动化用例和人工复现时省事。",
			Status:      StatusMissing,
			Installable: true,
			Note:        "可选：从 GitHub 最新 release 装到 " + filepath.Join(ToolsDir(), "scrcpy"),
		})
	}

	report.Deps = append(report.Deps, Dep{
		ID: "usb-driver", Name: "Android USB 驱动", Required: false,
		Description: "Windows 识别不到手机时用；不同厂商（Google、小米、OPPO、vivo…）各装各的。",
		Status:      StatusManual,
		Installable: false,
		Note:        "需手动安装：QAWork 不代装驱动，也不改系统驱动配置。装完在设备管理器里确认没有感叹号。",
	})
	report.Summary = summarize(report.Deps)
	return report
}

// ScrcpyPath is the scrcpy QAWork will use: bundled copy first, then PATH.
func ScrcpyPath() string {
	if p := filepath.Join(ToolsDir(), "scrcpy", exe("scrcpy")); fileExists(p) {
		return p
	}
	if p, err := exec.LookPath("scrcpy"); err == nil {
		return p
	}
	return ""
}

func adbPathNote(p string) string {
	if pathHasDir(filepath.Dir(p)) {
		return "PATH 可调用，GUI 和 CLI 直接用"
	}
	return "已安装，但当前 PATH 没有它；QAWork 会直接用绝对路径"
}

func summarize(deps []Dep) string {
	ok, missing, manual := 0, 0, 0
	for _, d := range deps {
		switch d.Status {
		case StatusOK:
			ok++
		case StatusMissing:
			missing++
		case StatusManual:
			manual++
		}
	}
	out := "已装 " + strconv.Itoa(ok) + " 项"
	if missing > 0 {
		out += "，缺 " + strconv.Itoa(missing) + " 项可安装"
	}
	if manual > 0 {
		out += "，" + strconv.Itoa(manual) + " 项需手动"
	}
	return out
}

// probeVersion runs a tool's version command and returns its first line.
func probeVersion(ctx context.Context, bin string, args ...string) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil && len(out) == 0 {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// FindDep returns the dependency with id, from a report.
func FindDep(r Report, id string) (Dep, bool) {
	for _, d := range r.Deps {
		if d.ID == id {
			return d, true
		}
	}
	return Dep{}, false
}
