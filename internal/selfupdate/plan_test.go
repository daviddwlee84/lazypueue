package selfupdate

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime/debug"
	"testing"
)

func TestCheckedPlanPinsRelease(t *testing.T) {
	_, opts := updateFixture(t)
	calls := 0
	opts.latest = func(context.Context) (Release, error) { calls++; return Release{Version: "v0.1.2"}, nil }
	p, err := check(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.latest = func(context.Context) (Release, error) { t.Fatal("apply re-resolved latest"); return Release{}, nil }
	result, err := apply(context.Background(), p, ApplyOptions{}, io.Discard, opts)
	if err != nil || result.Status != "updated" || calls != 1 {
		t.Fatalf("apply: %+v %v", result, err)
	}
}
func TestSourceUnavailableDoesNotWrite(t *testing.T) {
	i, opts := updateFixture(t)
	opts.latest = func(context.Context) (Release, error) { return Release{}, ErrSourceUnavailable }
	p, err := check(context.Background(), opts)
	if err != nil || p.Status != "source-unavailable" || p.CanUpgrade {
		t.Fatalf("check: %+v %v", p, err)
	}
	_, err = apply(context.Background(), p, ApplyOptions{Force: true}, io.Discard, opts)
	if !errors.Is(err, ErrSourceUnavailable) {
		t.Fatal(err)
	}
	requireOriginal(t, i)
}
func TestChangedDestinationSinceCheckIsPreserved(t *testing.T) {
	i, opts := updateFixture(t)
	p, err := check(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(i.ResolvedPath, []byte("another update"), 0750); err != nil {
		t.Fatal(err)
	}
	_, err = apply(context.Background(), p, ApplyOptions{}, io.Discard, opts)
	if err == nil {
		t.Fatal("changed destination replaced")
	}
	data, _ := os.ReadFile(i.ResolvedPath)
	if string(data) != "another update" {
		t.Fatal(string(data))
	}
}
func TestVersionFromSourceInstallation(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "v0.2.1"}}
	if v := ResolveVersion("dev", info); v != "v0.2.1" {
		t.Fatal(v)
	}
	if v := ResolveVersion("v9.0.0", info); v != "v9.0.0" {
		t.Fatal(v)
	}
	info.Main.Version = "v0.0.0-20260901000000-abcdefabcdef"
	if v := ResolveVersion("", info); v != info.Main.Version {
		t.Fatal(v)
	}
	if v := ResolveVersion("dev", nil); v != "dev" {
		t.Fatal(v)
	}
}
