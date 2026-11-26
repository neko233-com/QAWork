//go:build darwin

package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	pathBlockStart = "# >>> QAWork tools >>>"
	pathBlockEnd   = "# <<< QAWork tools <<<"
)

// AddUserPath appends dirs to ~/.zprofile and this process's PATH. It needs
// no sudo and never touches /etc or system-wide shell config. macOS Terminal
// starts a login zsh, so a new window picks the change up.
func AddUserPath(dirs ...string) error {
	if len(dirs) == 0 {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("找不到用户目录: %w", err)
	}
	profile := filepath.Join(home, ".zprofile")
	existing, err := os.ReadFile(profile)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	updated, changed := updatePathBlock(string(existing), dirs)
	if changed {
		if err := os.WriteFile(profile, []byte(updated), 0o644); err != nil {
			return err
		}
	}
	EnsurePATH(dirs...)
	return nil
}

// updatePathBlock replaces the managed block, or appends one. Directories
// already listed are not duplicated, so repeated installs stay idempotent.
func updatePathBlock(existing string, dirs []string) (string, bool) {
	var clean []string
	seen := map[string]bool{}
	for _, d := range dirs {
		d = filepath.Clean(strings.TrimSpace(d))
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		clean = append(clean, d)
	}
	if len(clean) == 0 {
		return existing, false
	}
	line := `export PATH="$PATH:` + strings.Join(clean, ":") + `"`
	block := pathBlockStart + "\n" + line + "\n" + pathBlockEnd + "\n"

	if start := strings.Index(existing, pathBlockStart); start >= 0 {
		if end := strings.Index(existing[start:], pathBlockEnd); end >= 0 {
			end += start + len(pathBlockEnd)
			for end < len(existing) && (existing[end] == '\n' || existing[end] == '\r') {
				end++
			}
			if strings.TrimSpace(existing[start:end]) == strings.TrimSpace(block) {
				return existing, false
			}
			return existing[:start] + block + existing[end:], true
		}
	}
	out := existing
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	out += "\n" + block
	return out, true
}

// UserPathHas reports whether dir is already on PATH.
func UserPathHas(dir string) bool {
	for _, part := range splitPath(os.Getenv("PATH")) {
		if part == dir {
			return true
		}
	}
	return false
}

func splitPath(path string) []string {
	var out []string
	start := 0
	for i := 0; i < len(path); i++ {
		if path[i] == os.PathListSeparator {
			out = append(out, path[start:i])
			start = i + 1
		}
	}
	return append(out, path[start:])
}
