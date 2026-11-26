package stress

import (
	"context"
	"strings"
	"testing"
)

func TestRunRejectsBadPackage(t *testing.T) {
	_, err := Run(context.Background(), Options{Package: "bad pkg"})
	if err == nil || !strings.Contains(err.Error(), "包名无效") {
		t.Fatalf("err = %v", err)
	}
}

func TestExcerpt(t *testing.T) {
	if got := excerpt("  abcdef  ", 3); got != "abc…" {
		t.Errorf("excerpt = %q", got)
	}
	if got := excerpt("abc", 10); got != "abc" {
		t.Errorf("excerpt = %q", got)
	}
}
