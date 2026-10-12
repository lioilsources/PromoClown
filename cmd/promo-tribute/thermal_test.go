package main

import (
	"bytes"
	"context"
	"errors"
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
	g := newThermalGuard(path, defaultHotC, defaultCoolC, time.Minute, &bytes.Buffer{})
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
	g := newThermalGuard(path, defaultHotC, defaultCoolC, time.Minute, &log)
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

	// A hot box that stays hot ends the wait with errThermalWait.
	g.maxWait = 30 * time.Millisecond
	if err := g.wait(context.Background()); !errors.Is(err, errThermalWait) {
		t.Errorf("95 °C for the whole wait: err = %v, want errThermalWait", err)
	}

	// So does a log that cannot be read: a dead logger is not a cool box.
	os.Remove(path)
	if err := g.wait(context.Background()); !errors.Is(err, errThermalWait) {
		t.Errorf("missing log: err = %v, want errThermalWait", err)
	}

	// A reading that comes back cool lets the run on.
	g.maxWait = 2 * time.Second
	go func() { time.Sleep(20 * time.Millisecond); write(80) }()
	if err := g.wait(context.Background()); err != nil {
		t.Errorf("log came back at 80 °C: %v", err)
	}

	// No guard, no wait.
	var none *thermalGuard
	if err := none.wait(context.Background()); err != nil {
		t.Errorf("nil guard: %v", err)
	}
	off := newThermalGuard("", defaultHotC, defaultCoolC, time.Minute, &log)
	if err := off.wait(context.Background()); err != nil {
		t.Errorf("empty path: %v", err)
	}
}

func TestThermalEvent(t *testing.T) {
	dir := t.TempDir()
	g := newThermalGuard(filepath.Join(dir, "thermal.csv"), defaultHotC, defaultCoolC, time.Minute, &bytes.Buffer{})
	g.now = func() time.Time { return time.Date(2026, 10, 11, 11, 30, 3, 0, time.Local) }
	events := filepath.Join(dir, "thermal-events.csv")

	// No events file on this machine: nothing is created.
	g.event("start (18 tributů)")
	if _, err := os.Stat(events); err == nil {
		t.Fatal("the events file must not be created")
	}

	os.WriteFile(events, []byte("ts,note\n"), 0o644)
	g.event("start (18 tributů)")
	g.event("konec (18 hotovo, 0 chyb)")
	got, _ := os.ReadFile(events)
	want := "ts,note\n" +
		"2026/10/11 11:30:03.000,\"promo-tribute: start (18 tributů)\"\n" +
		"2026/10/11 11:30:03.000,\"promo-tribute: konec (18 hotovo, 0 chyb)\"\n"
	if string(got) != want {
		t.Errorf("events =\n%s\nwant\n%s", got, want)
	}
}
