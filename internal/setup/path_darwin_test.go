//go:build darwin

package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdatePathBlock(t *testing.T) {
	dirA := "/Users/me/Library/Application Support/QAWork/tools/platform-tools"
	dirB := "/Users/me/Library/Application Support/QAWork/tools/scrcpy"
	out, changed := updatePathBlock("", []string{dirA, dirB})
	if !changed || !strings.Contains(out, pathBlockStart) || !strings.Contains(out, dirA+":"+dirB) {
		t.Errorf("block = %q changed=%v", out, changed)
	}
	again, changed := updatePathBlock(out, []string{dirA, dirB})
	if changed || again != out {
		t.Errorf("must be idempotent, changed=%v", changed)
	}
	updated, changed := updatePathBlock(out, []string{dirB})
	if !changed || !strings.Contains(updated, dirB) || strings.Contains(updated, "platform-tools") {
		t.Errorf("updated = %q", updated)
	}
}

func TestAddUserPathDarwin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "tools", "platform-tools")
	if err := AddUserPath(dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, ".zprofile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), dir) {
		t.Errorf("profile = %q", b)
	}
	if !UserPathHas(dir) {
		t.Error("process PATH was not updated")
	}
	if err := AddUserPath(dir); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(filepath.Join(home, ".zprofile"))
	if string(b2) != string(b) {
		t.Errorf("second install rewrote profile:\n%s\n---\n%s", b, b2)
	}
}
