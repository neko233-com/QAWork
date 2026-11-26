package setup

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendPath(t *testing.T) {
	got, changed := appendPath(`C:\Windows;C:\Tools`, []string{`c:\tools`, `C:\adb`}, ";")
	if !changed {
		t.Fatal("want changed")
	}
	if got != `C:\Windows;C:\Tools;C:\adb` {
		t.Errorf("path = %q", got)
	}
	if got, changed := appendPath(got, []string{`C:\ADB`}, ";"); changed || got != `C:\Windows;C:\Tools;C:\adb` {
		t.Errorf("duplicate changed=%v path=%q", changed, got)
	}
	if got, changed := appendPath("", []string{`C:\adb`}, ";"); !changed || got != `C:\adb` {
		t.Errorf("empty changed=%v path=%q", changed, got)
	}
	if got, changed := appendPath(`C:\a;`, []string{`C:\b`}, ";"); !changed || got != `C:\a;C:\b` {
		t.Errorf("trailing sep changed=%v path=%q", changed, got)
	}
}

func TestADBPathFromEnv(t *testing.T) {
	fake := filepath.Join(t.TempDir(), exe("adb"))
	if err := os.WriteFile(fake, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QAWORK_ADB", fake)
	t.Setenv("QAWORK_HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("LOCALAPPDATA", t.TempDir())
	if got := ADBPath(); got != fake {
		t.Errorf("ADBPath = %q, want %q", got, fake)
	}
	// A stale QAWORK_ADB must not win over nothing.
	t.Setenv("QAWORK_ADB", filepath.Join(t.TempDir(), "missing.exe"))
	t.Setenv("QAWORK_HOME", filepath.Join(t.TempDir(), "home2"))
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Setenv("PATH", "")
	if got := ADBPath(); got != "" {
		t.Errorf("ADBPath = %q, want empty", got)
	}
}

func TestInspectBundledADB(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QAWORK_HOME", home)
	t.Setenv("QAWORK_ADB", "")
	t.Setenv("PATH", "")
	dir := filepath.Join(home, "tools", "platform-tools")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, exe("adb")), []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	report := Inspect(context.Background())
	if report.ADBPath == "" {
		t.Fatal("want bundled adb")
	}
	d, ok := FindDep(report, "adb")
	if !ok || d.Status != StatusOK || !d.Required {
		t.Errorf("adb dep = %+v", d)
	}
	if _, ok := FindDep(report, "scrcpy"); !ok {
		t.Error("scrcpy row missing")
	}
	driver, ok := FindDep(report, "usb-driver")
	if !ok || driver.Status != StatusManual || driver.Installable {
		t.Errorf("driver = %+v", driver)
	}
	if report.Summary == "" {
		t.Error("summary empty")
	}
}

func TestToolsDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QAWORK_HOME", home)
	if got := ToolsDir(); got != filepath.Join(home, "tools") {
		t.Errorf("ToolsDir = %q", got)
	}
}

func TestExtractZip(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "tools.zip")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"platform-tools/adb.exe":           "adb",
		"platform-tools/lib/AdbWinApi.dll": "dll",
		"platform-tools/README.txt":        "readme",
		"other/ignored.txt":                "nope",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zipPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out")
	if err := extractZip(zipPath, dst, "platform-tools"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "adb.exe")); err != nil || string(b) != "adb" {
		t.Errorf("adb excerpt = %q err=%v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dst, "lib", "AdbWinApi.dll")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "ignored.txt")); err == nil {
		t.Error("entry outside prefix should be skipped")
	}
}

func TestExtractZipRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "evil.zip")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("platform-tools/../evil.txt")
	w.Write([]byte("x"))
	zw.Close()
	if err := os.WriteFile(zipPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractZip(zipPath, filepath.Join(dir, "out"), "platform-tools"); err == nil {
		t.Fatal("want traversal error")
	}
}

func TestExtractZipWithoutPrefix(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "scrcpy.zip")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("scrcpy-win64-v3.3.1/scrcpy.exe")
	w.Write([]byte("s"))
	zw.Close()
	if err := os.WriteFile(zipPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out")
	if err := extractZip(zipPath, dst, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "scrcpy.exe")); err != nil {
		t.Error(err)
	}
}

func TestPickScrcpyAsset(t *testing.T) {
	var r release
	r.TagName = "v3.3.1"
	r.Assets = []asset{
		{Name: "scrcpy-win64-v3.3.1.zip", URL: "https://example.com/win64.zip"},
		{Name: "scrcpy-linux.zip", URL: "https://example.com/linux.zip"},
	}
	url, tag, err := pickScrcpyAsset(r)
	if err != nil || url != "https://example.com/win64.zip" || tag != "v3.3.1" {
		t.Errorf("asset = %q %q %v", url, tag, err)
	}
	r.Assets = nil
	if _, _, err := pickScrcpyAsset(r); err == nil {
		t.Error("want error without win64 asset")
	}
}

func TestDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "QAWork-setup" {
			t.Errorf("user agent = %q", r.Header.Get("User-Agent"))
		}
		w.Write([]byte("payload"))
	}))
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "out.bin")
	var got int64
	if err := download(context.Background(), srv.URL, dst, func(n, total int64) { got = n }); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dst)
	if string(b) != "payload" || got != 7 {
		t.Errorf("body=%q progress=%d", b, got)
	}
}

func TestDownloadHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	if err := download(context.Background(), srv.URL, filepath.Join(t.TempDir(), "x"), nil); err == nil {
		t.Fatal("want HTTP error")
	}
}

func TestInstallUnknown(t *testing.T) {
	if _, err := Install(context.Background(), "nope", false, nil); err == nil {
		t.Fatal("want error")
	}
	if _, err := Install(context.Background(), "usb-driver", false, nil); err == nil ||
		!strings.Contains(err.Error(), "手动") {
		t.Errorf("driver install error = %v", err)
	}
}
