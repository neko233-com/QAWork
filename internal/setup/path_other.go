//go:build !windows && !darwin

package setup

import (
	"errors"
	"os"
)

// AddUserPath is only wired on Windows and macOS, QAWork's release targets.
func AddUserPath(dirs ...string) error {
	return errors.New("环境安装目前只支持 Windows 与 macOS")
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
