//go:build windows

package setup

import (
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// AddUserPath appends dirs to HKCU\Environment\Path and to this process's
// PATH. It never needs admin rights and never touches the machine-wide
// environment.
func AddUserPath(dirs ...string) error {
	if len(dirs) == 0 {
		return nil
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	existing, valtype, err := key.GetStringValue("Path")
	if err != nil && err != registry.ErrNotExist {
		return err
	}
	existing = os.ExpandEnv(existing)
	updated, changed := appendPath(existing, dirs, ";")
	if changed {
		if valtype == registry.EXPAND_SZ {
			err = key.SetExpandStringValue("Path", updated)
		} else {
			err = key.SetStringValue("Path", updated)
		}
		if err != nil {
			return err
		}
	}
	process, _ := appendPath(os.Getenv("PATH"), dirs, ";")
	if process != os.Getenv("PATH") {
		_ = os.Setenv("PATH", process)
	}
	return nil
}

// UserPathHas reports whether dir is already on the user's PATH.
func UserPathHas(dir string) bool {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()
	path, _, err := key.GetStringValue("Path")
	if err != nil {
		return false
	}
	path = os.ExpandEnv(path)
	for _, part := range strings.Split(path, ";") {
		if strings.EqualFold(strings.TrimSpace(part), strings.TrimSpace(dir)) {
			return true
		}
	}
	return false
}
