package setup

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	scrcpyAPI = "https://api.github.com/repos/Genymobile/scrcpy/releases/latest"
)

var platformToolsURL = platformToolsURLFor(runtime.GOOS)

// platformToolsURLFor picks Google's official zip for the host.
func platformToolsURLFor(goos string) string {
	if goos == "darwin" {
		return "https://dl.google.com/android/repository/platform-tools-latest-darwin.zip"
	}
	return "https://dl.google.com/android/repository/platform-tools-latest-windows.zip"
}

// Install installs one dependency by id: adb or scrcpy. addPath decides
// whether the installed directory is appended to the current user's PATH.
// Progress lines are meant for the UI and the CLI.
func Install(ctx context.Context, id string, addPath bool, progress func(string)) (Dep, error) {
	if progress == nil {
		progress = func(string) {}
	}
	switch id {
	case "adb":
		return installADB(ctx, addPath, progress)
	case "scrcpy":
		return installScrcpy(ctx, addPath, progress)
	case "usb-driver":
		return Dep{}, fmt.Errorf("USB 驱动需要手动安装，QAWork 不代装")
	default:
		return Dep{}, fmt.Errorf("未知依赖 %q", id)
	}
}

func installADB(ctx context.Context, addPath bool, progress func(string)) (Dep, error) {
	if ADBPath() != "" {
		progress("adb 已可用，跳过下载")
		if d, ok := FindDep(Inspect(ctx), "adb"); ok {
			return d, nil
		}
	}
	dir := filepath.Join(ToolsDir(), "platform-tools")
	adb := filepath.Join(dir, exe("adb"))
	if fileExists(adb) {
		progress("adb 已在 " + dir + "，跳过下载")
	} else {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Dep{}, err
		}
		progress("下载 platform-tools（Google 官方）…")
		zipPath := filepath.Join(os.TempDir(), "qawork-platform-tools.zip")
		if err := download(ctx, platformToolsURL, zipPath, nil); err != nil {
			return Dep{}, fmt.Errorf("下载 platform-tools: %w", err)
		}
		defer os.Remove(zipPath)
		progress("解压到 " + dir + " …")
		if err := extractZip(zipPath, dir, "platform-tools"); err != nil {
			return Dep{}, fmt.Errorf("解压 platform-tools: %w", err)
		}
	}
	if !fileExists(adb) {
		return Dep{}, fmt.Errorf("解压后没有找到 %s", adb)
	}
	markExecutable(dir)
	if addPath {
		progress("写入当前用户 PATH…")
		if err := AddUserPath(dir); err != nil {
			return Dep{}, fmt.Errorf("写 PATH: %w", err)
		}
	}
	progress("adb 安装完成")
	report := Inspect(ctx)
	if d, ok := FindDep(report, "adb"); ok {
		return d, nil
	}
	return Dep{ID: "adb", Name: "adb", Status: StatusOK, Path: adb, Installable: true}, nil
}

type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type release struct {
	TagName string  `json:"tag_name"`
	Assets  []asset `json:"assets"`
}

func installScrcpy(ctx context.Context, addPath bool, progress func(string)) (Dep, error) {
	if ScrcpyPath() != "" {
		progress("scrcpy 已可用，跳过下载")
		if d, ok := FindDep(Inspect(ctx), "scrcpy"); ok {
			return d, nil
		}
	}
	dir := filepath.Join(ToolsDir(), "scrcpy")
	if fileExists(filepath.Join(dir, exe("scrcpy"))) {
		progress("scrcpy 已在 " + dir + "，跳过下载")
	} else {
		progress("查询 scrcpy 最新 release…")
		assetURL, tag, err := latestScrcpy(ctx)
		if err != nil {
			return Dep{}, err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Dep{}, err
		}
		progress("下载 " + tag + " …")
		zipPath := filepath.Join(os.TempDir(), "qawork-scrcpy.zip")
		if err := download(ctx, assetURL, zipPath, nil); err != nil {
			return Dep{}, fmt.Errorf("下载 scrcpy: %w", err)
		}
		defer os.Remove(zipPath)
		progress("解压到 " + dir + " …")
		if err := extractZip(zipPath, dir, ""); err != nil {
			return Dep{}, fmt.Errorf("解压 scrcpy: %w", err)
		}
	}
	if !fileExists(filepath.Join(dir, exe("scrcpy"))) {
		return Dep{}, fmt.Errorf("解压后没有找到 scrcpy，release 结构可能变了")
	}
	markExecutable(dir)
	if addPath {
		progress("写入当前用户 PATH…")
		if err := AddUserPath(dir); err != nil {
			return Dep{}, fmt.Errorf("写 PATH: %w", err)
		}
	}
	progress("scrcpy 安装完成")
	report := Inspect(ctx)
	if d, ok := FindDep(report, "scrcpy"); ok {
		return d, nil
	}
	return Dep{ID: "scrcpy", Name: "scrcpy", Status: StatusOK, Installable: true}, nil
}

// latestScrcpy picks the zip matching this host.
func latestScrcpy(ctx context.Context) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scrcpyAPI, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "QAWork-setup")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("查询 GitHub release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("GitHub release 返回 %s", resp.Status)
	}
	var r release
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", "", fmt.Errorf("解析 GitHub release: %w", err)
	}
	return pickScrcpyAsset(r)
}

// pickScrcpyAsset selects the asset for this host.
func pickScrcpyAsset(r release) (string, string, error) {
	return pickScrcpyAssetFor(r, runtime.GOOS, runtime.GOARCH)
}

// pickScrcpyAssetFor selects win64 on Windows and macos-aarch64 on Apple
// Silicon. A plain macos zip is accepted only if no arm64 asset exists.
func pickScrcpyAssetFor(r release, goos, goarch string) (string, string, error) {
	var fallback asset
	for _, a := range r.Assets {
		name := strings.ToLower(a.Name)
		if !strings.HasSuffix(name, ".zip") {
			continue
		}
		switch goos {
		case "windows":
			if strings.Contains(name, "win64") {
				return a.URL, r.TagName, nil
			}
		case "darwin":
			if !strings.Contains(name, "macos") {
				continue
			}
			if strings.Contains(name, "aarch64") || strings.Contains(name, "arm64") {
				return a.URL, r.TagName, nil
			}
			if !strings.Contains(name, "x86") && !strings.Contains(name, "intel") {
				fallback = a
			}
		}
	}
	if fallback.URL != "" && goarch == "arm64" {
		return fallback.URL, r.TagName, nil
	}
	return "", "", fmt.Errorf("release %s 没有 %s/%s 的 zip", r.TagName, goos, goarch)
}

// markExecutable restores the +x bit lost by archive extraction. Windows
// ignores modes, so this is a no-op there.
func markExecutable(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		_ = os.Chmod(path, 0o755)
		return nil
	})
}

// download fetches url to dst and reports bytes as they arrive.
func download(ctx context.Context, url, dst string, progress func(int64, int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "QAWork-setup")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	var n int64
	buf := make([]byte, 32*1024)
	for {
		k, rerr := resp.Body.Read(buf)
		if k > 0 {
			if _, werr := f.Write(buf[:k]); werr != nil {
				return werr
			}
			n += int64(k)
			if progress != nil {
				progress(n, resp.ContentLength)
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

// extractZip extracts entries under prefix into dst, stripping the prefix
// component. An empty prefix keeps the entry path relative to dst, skipping
// a single top-level directory when that is how the archive is packed.
func extractZip(src, dst, prefix string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	cleanDst, err := filepath.Abs(dst)
	if err != nil {
		return err
	}
	var top string
	if prefix == "" {
		for _, f := range zr.File {
			name := filepath.ToSlash(f.Name)
			if i := strings.Index(name, "/"); i > 0 {
				top = name[:i]
				break
			}
		}
	}
	for _, f := range zr.File {
		name := filepath.ToSlash(f.Name)
		if prefix != "" {
			if !strings.HasPrefix(name, prefix+"/") {
				continue
			}
			name = strings.TrimPrefix(name, prefix+"/")
		} else if top != "" && strings.HasPrefix(name, top+"/") {
			name = strings.TrimPrefix(name, top+"/")
		}
		if name == "" || strings.HasSuffix(name, "/") {
			continue
		}
		rel := filepath.FromSlash(name)
		if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "..") {
			return fmt.Errorf("压缩包条目越界: %q", f.Name)
		}
		target := filepath.Join(cleanDst, rel)
		if !strings.HasPrefix(target, cleanDst+string(filepath.Separator)) {
			return fmt.Errorf("压缩包条目越界: %q", f.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// appendPath appends missing dirs to a PATH value, keeping the separators
// the platform uses. It is pure so the Windows registry code stays thin.
func appendPath(existing string, dirs []string, sep string) (string, bool) {
	parts := strings.Split(existing, sep)
	seen := make(map[string]bool, len(parts))
	for _, p := range parts {
		if p != "" {
			seen[strings.ToLower(filepath.Clean(p))] = true
		}
	}
	changed := false
	out := existing
	for _, d := range dirs {
		d = filepath.Clean(d)
		if d == "" || seen[strings.ToLower(d)] {
			continue
		}
		if out != "" && !strings.HasSuffix(out, sep) {
			out += sep
		}
		out += d
		seen[strings.ToLower(d)] = true
		changed = true
	}
	return out, changed
}

// EnsurePATH puts dirs on the process PATH immediately so this run can use
// them without reopening a terminal.
func EnsurePATH(dirs ...string) {
	sep := string(os.PathListSeparator)
	path, changed := appendPath(os.Getenv("PATH"), dirs, sep)
	if changed {
		_ = os.Setenv("PATH", path)
	}
}

// pathHasDir reports whether PATH already reaches dir.
func pathHasDir(dir string) bool {
	_, changed := appendPath(os.Getenv("PATH"), []string{dir}, string(os.PathListSeparator))
	return !changed
}
