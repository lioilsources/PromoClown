package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const thermalHeader = "ts,gpu_temp_c,sm_clock_mhz,sm_clock_max_mhz,power_w,throttle_hw_slowdown,throttle_hw_thermal,throttle_sw_thermal,zone_max_c,ctr_sw_thermal_us,ctr_hw_thermal_us,ctr_sw_power_cap_us\n"

func thermalRow(ts time.Time, zone float64) string {
	return fmt.Sprintf("%s.924,82,2372,3003,85.25,Not Active,Not Active,Not Active,%.1f,49214536,3828188,602235\n",
		ts.Format("2006/01/02 15:04:05"), zone)
}

func TestThermalZone(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.Local)
	path := filepath.Join(t.TempDir(), "thermal.csv")
	g := newThermalGuard(path, defaultHotC, defaultCoolC, &bytes.Buffer{})
	g.now = func() time.Time { return now }

	if _, ok := g.zone(); ok {
		t.Error("a missing log must not give a reading")
	}

	// The last row counts, also when the file is longer than the tail read.
	body := thermalHeader + strings.Repeat(thermalRow(now.Add(-time.Hour), 70), 20) + thermalRow(now.Add(-5*time.Second), 94.6)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if zone, ok := g.zone(); !ok || zone != 94.6 {
		t.Errorf("zone = %v, %v; want 94.6, true", zone, ok)
	}

	// The early rows of the real log stop at column 9.
	short := "2026/10/11 11:59:58.205,73,2470,3003,80.31,Not Active,Not Active,Not Active,84.6\n"
	os.WriteFile(path, []byte(thermalHeader+short), 0o644)
	if zone, ok := g.zone(); !ok || zone != 84.6 {
		t.Errorf("nine-column row: zone = %v, %v; want 84.6, true", zone, ok)
	}

	os.WriteFile(path, []byte(thermalHeader+thermalRow(now.Add(-2*time.Minute), 96)), 0o644)
	if _, ok := g.zone(); ok {
		t.Error("a row two minutes old must not be believed")
	}

	os.WriteFile(path, []byte(thermalHeader), 0o644)
	if _, ok := g.zone(); ok {
		t.Error("a log with only the header must not give a reading")
	}
}

func TestThermalWait(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.Local)
	path := filepath.Join(t.TempDir(), "thermal.csv")
	write := func(zone float64) {
		t.Helper()
		// Renamed into place: the waiting goroutine must never read a half-written log.
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, []byte(thermalHeader+thermalRow(now, zone)), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, path); err != nil {
			t.Fatal(err)
		}
	}
	var log bytes.Buffer
	g := newThermalGuard(path, defaultHotC, defaultCoolC, &log)
	g.now = func() time.Time { return now }
	g.poll = time.Millisecond

	// Warm but under the hot mark: no pause, even though it is above the cool one.
	write(92.9)
	if err := g.wait(context.Background()); err != nil || log.Len() != 0 {
		t.Errorf("92.9 °C: err = %v, log = %q; want straight through", err, log.String())
	}

	// Hot: blocks until the zone is under the cool mark, not merely under the hot one.
	write(93)
	done := make(chan error, 1)
	go func() { done <- g.wait(context.Background()) }()
	time.Sleep(20 * time.Millisecond)
	write(88)
	select {
	case <-done:
		t.Fatal("resumed at 88 °C, above the cool mark")
	case <-time.After(20 * time.Millisecond):
	}
	write(86.9)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("still paused at 86.9 °C")
	}
	if got := log.String(); !strings.Contains(got, "thermal pause: zone 93.0") || !strings.Contains(got, "thermal resume: zone 86.9") {
		t.Errorf("log = %q", got)
	}

	// A cancelled run stops waiting.
	write(95)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.wait(ctx); err == nil {
		t.Error("a cancelled context must end the wait with an error")
	}

	// No guard, no wait.
	var none *thermalGuard
	if err := none.wait(context.Background()); err != nil {
		t.Errorf("nil guard: %v", err)
	}
}
