package adbkit

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseGetprop(t *testing.T) {
	out := `[ro.product.model]: [BVL-AN16]
[ro.product.brand]: [honor]
[ro.build.version.release]: [15]
[ro.build.version.sdk]: [35]
[ro.product.cpu.abi]: [arm64-v8a]
[malformed]
`
	m := ParseGetprop(out)
	if m["ro.product.model"] != "BVL-AN16" || m["ro.product.brand"] != "honor" {
		t.Fatalf("props = %#v", m)
	}
	if m["ro.build.version.sdk"] != "35" || m["ro.product.cpu.abi"] != "arm64-v8a" {
		t.Fatalf("props = %#v", m)
	}
}

func TestParseWMDensityAndBattery(t *testing.T) {
	if got := ParseResolution("Physical size: 1080x1920\nOverride size: 720x1280\n"); got != "720x1280" {
		t.Errorf("resolution = %q", got)
	}
	if got := ParseResolution("Physical size: 1080x1920\n"); got != "1080x1920" {
		t.Errorf("resolution = %q", got)
	}
	if got := ParseDensity("Physical density: 420\nOverride density: 560\n"); got != "560" {
		t.Errorf("density = %q", got)
	}
	level, temp := ParseBattery("level: 87\ntemperature: 321\n")
	if level != 87 || temp != 32.1 {
		t.Errorf("battery = %d, %.1f", level, temp)
	}
}

func TestParseCurrentActivity(t *testing.T) {
	window := "  mCurrentFocus=Window{abc u0 com.android.settings/com.android.settings.Settings}\n"
	if got := ParseCurrentActivity(window); got != "com.android.settings/com.android.settings.Settings" {
		t.Errorf("window component = %q", got)
	}
	activity := "  mResumedActivity: ActivityRecord{123 u0 com.example.game/.MainActivity t42}\n"
	if got := ParseCurrentActivity(activity); got != "com.example.game/.MainActivity" {
		t.Errorf("activity component = %q", got)
	}
	if got := ParseCurrentActivity("nothing here"); got != "" {
		t.Errorf("empty parse = %q", got)
	}
}

func TestActivityMatches(t *testing.T) {
	cases := []struct {
		current, want string
		ok            bool
	}{
		{"com.example.game/.MainActivity", "com.example.game/.MainActivity", true},
		{"com.example.game/com.example.game.MainActivity", "com.example.game/.MainActivity", true},
		{"com.example.game/.MainActivity", ".MainActivity", true},
		{"com.example.game/.MainActivity", "MainActivity", true},
		{"com.example.game/.MainActivity", "com.other/.MainActivity", false},
		{"com.example.game/.MainActivity", "com.example", true},
		{"com.example.game/.MainActivity", "com.example.game.Other", false},
	}
	for _, c := range cases {
		if got := ActivityMatches(c.current, c.want); got != c.ok {
			t.Errorf("ActivityMatches(%q,%q)=%v want %v", c.current, c.want, got, c.ok)
		}
	}
}

func TestUITextsAndHasText(t *testing.T) {
	xmlText := `<hierarchy><node text="登录" content-desc="close"/><node text=""/><node text="Start Game"/></hierarchy>`
	texts := UITexts(xmlText)
	if len(texts) != 3 || texts[0] != "登录" || texts[1] != "close" || texts[2] != "Start Game" {
		t.Fatalf("texts = %#v", texts)
	}
	if !UIHasText(xmlText, "Start") || !UIHasText(xmlText, "登录") {
		t.Error("UIHasText missed text")
	}
	if UIHasText(xmlText, "缺失") {
		t.Error("UIHasText matched missing text")
	}
}

func TestValidNames(t *testing.T) {
	for _, pkg := range []string{"com.example.game", "com.example_game", "a.b"} {
		if !ValidPackage(pkg) {
			t.Errorf("ValidPackage(%q)=false", pkg)
		}
	}
	for _, pkg := range []string{"", "com example", "com-example", "com.", ".com"} {
		if ValidPackage(pkg) {
			t.Errorf("ValidPackage(%q)=true", pkg)
		}
	}
	for _, p := range []string{"android.permission.CAMERA", "com.example.permission.X"} {
		if !ValidPermission(p) {
			t.Errorf("ValidPermission(%q)=false", p)
		}
	}
	if ValidPermission("CAMERA") {
		t.Error("ValidPermission accepted a bare name")
	}
}

type fakeSheller struct {
	out  string
	err  error
	cmds []string
}

func (f *fakeSheller) Shell(_ context.Context, cmd string) (string, error) {
	f.cmds = append(f.cmds, cmd)
	return f.out, f.err
}

func TestCurrentActivityFallsBackToActivities(t *testing.T) {
	f := &seqSheller{outs: []string{
		"mCurrentFocus=Window{1}\n",
		"  mResumedActivity: ActivityRecord{1 u0 com.example.game/.Main t1}\n",
	}}
	act, err := CurrentActivity(context.Background(), f)
	if err != nil || act != "com.example.game/.Main" {
		t.Fatalf("CurrentActivity = %q, %v", act, err)
	}
	if len(f.cmds) != 2 {
		t.Fatalf("commands = %#v", f.cmds)
	}
}

type seqSheller struct {
	outs []string
	cmds []string
}

func (s *seqSheller) Shell(_ context.Context, cmd string) (string, error) {
	s.cmds = append(s.cmds, cmd)
	if len(s.outs) == 0 {
		return "", nil
	}
	out := s.outs[0]
	s.outs = s.outs[1:]
	return out, nil
}

func TestDumpUIRetriesUncompressed(t *testing.T) {
	f := &dumpSheller{}
	xmlText, err := DumpUI(context.Background(), f)
	if err != nil || !strings.Contains(xmlText, "<hierarchy") {
		t.Fatalf("DumpUI = %q, %v", xmlText, err)
	}
	if f.dumpCalls != 2 {
		t.Errorf("dump calls = %d", f.dumpCalls)
	}
}

type dumpSheller struct {
	dumpCalls int
}

func (d *dumpSheller) Shell(_ context.Context, cmd string) (string, error) {
	switch {
	case strings.HasPrefix(cmd, "uiautomator dump --compressed"):
		d.dumpCalls++
		return "", errors.New("compressed unavailable")
	case strings.HasPrefix(cmd, "uiautomator dump"):
		d.dumpCalls++
		return "UI hierchary dumped to: /data/local/tmp/qawork-ui.xml", nil
	case strings.HasPrefix(cmd, "cat "):
		return "<hierarchy><node text=\"ok\"/></hierarchy>", nil
	default:
		return "", nil
	}
}
