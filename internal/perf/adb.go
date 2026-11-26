package perf

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ADB calls the Android platform tools. An empty Path uses "adb" from PATH,
// the field lets a test or a bundled binary point elsewhere.
type ADB struct {
	Path string
}

func (a ADB) bin() string {
	if a.Path != "" {
		return a.Path
	}
	return "adb"
}

// Exec runs one adb command and returns its combined output.
func (a ADB) Exec(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, a.bin(), args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return "", fmt.Errorf("adb %s: %w", strings.Join(args, " "), err)
		}
		return string(out), fmt.Errorf("adb %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return string(out), nil
}

// ExecBytes runs an adb command and returns raw stdout, for screenshots.
func (a ADB) ExecBytes(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, a.bin(), args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return nil, fmt.Errorf("adb %s: %w", strings.Join(args, " "), err)
		}
		return nil, fmt.Errorf("adb %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return out, nil
}

// Shell runs a shell command on one device.
func (a ADB) Shell(ctx context.Context, serial, command string) (string, error) {
	return a.Exec(ctx, "-s", serial, "shell", command)
}

// Device is one entry of "adb devices -l".
type Device struct {
	Serial  string `json:"serial"`
	State   string `json:"state"`
	Model   string `json:"model,omitempty"`
	Product string `json:"product,omitempty"`
}

// Label is the line the device list shows.
func (d Device) Label() string {
	name := d.Model
	if name == "" {
		name = d.Product
	}
	name = strings.ReplaceAll(name, "_", " ")
	if name == "" {
		name = d.Serial
	}
	if d.State != "" && d.State != "device" {
		return name + "（" + d.State + "）"
	}
	return name
}

// Devices lists the devices adb sees.
func (a ADB) Devices(ctx context.Context) ([]Device, error) {
	out, err := a.Exec(ctx, "devices", "-l")
	if err != nil {
		return nil, err
	}
	return ParseDevices(out), nil
}

// ParseDevices reads "adb devices -l" output.
func ParseDevices(out string) []Device {
	var devices []Device
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "List of devices") || strings.HasPrefix(line, "*") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		d := Device{Serial: fields[0], State: fields[1]}
		for _, f := range fields[2:] {
			switch {
			case strings.HasPrefix(f, "model:"):
				d.Model = strings.TrimPrefix(f, "model:")
			case strings.HasPrefix(f, "product:"):
				d.Product = strings.TrimPrefix(f, "product:")
			}
		}
		devices = append(devices, d)
	}
	return devices
}
