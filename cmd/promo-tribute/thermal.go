package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// The Spark's thermal logger appends a row to ~/ops/thermal.csv every 5 s;
// column 9 is the hottest thermal zone. Every batch on the box reads it, so
// that two of them in the same ComfyUI window do not cook it together: on
// 2026-10-11 the tributes ran alongside a Lab batch and held the zone at
// 92–96 °C for forty minutes, because the Lab paused for the heat and the
// tributes did not know to.
const (
	defaultHotC  = 93.0 // pause at or above
	defaultCoolC = 87.0 // and stay paused until below
	// thermalStale is how old the last row may be before it is not believed.
	thermalStale = time.Minute
)

type thermalGuard struct {
	path      string
	hot, cool float64
	poll      time.Duration
	now       func() time.Time
	log       io.Writer
}

func newThermalGuard(path string, hot, cool float64, log io.Writer) *thermalGuard {
	return &thermalGuard{path: path, hot: hot, cool: cool, poll: 15 * time.Second, now: time.Now, log: log}
}

// wait blocks while the box is hot: from the hot mark until the zone is back
// under the cool one. A missing or stale log is not a reason to stop (the
// logger is an ad-hoc script, and a dev machine has none), so it lets the
// caller through.
func (g *thermalGuard) wait(ctx context.Context) error {
	if g == nil || g.path == "" {
		return nil
	}
	zone, ok := g.zone()
	if !ok || zone < g.hot {
		return nil
	}
	fmt.Fprintf(g.log, "thermal pause: zone %.1f °C, waiting for under %.0f °C\n", zone, g.cool)
	for ok && zone >= g.cool {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(g.poll):
		}
		zone, ok = g.zone()
	}
	if !ok {
		fmt.Fprintln(g.log, "thermal resume: the thermal log went stale")
		return nil
	}
	fmt.Fprintf(g.log, "thermal resume: zone %.1f °C\n", zone)
	return nil
}

// zone is the hottest zone in the last row of the log, if that row is fresh.
func (g *thermalGuard) zone() (float64, bool) {
	f, err := os.Open(g.path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	// A row is about 110 bytes; the tail is enough for the last complete one.
	const tail = 512
	if st, err := f.Stat(); err == nil && st.Size() > tail {
		if _, err := f.Seek(-tail, io.SeekEnd); err != nil {
			return 0, false
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return 0, false
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	cols := strings.Split(lines[len(lines)-1], ",")
	if len(cols) < 9 || len(cols[0]) < 19 {
		return 0, false
	}
	ts, err := time.ParseInLocation("2006/01/02 15:04:05", cols[0][:19], time.Local)
	if err != nil || g.now().Sub(ts) > thermalStale {
		return 0, false
	}
	zone, err := strconv.ParseFloat(strings.TrimSpace(cols[8]), 64)
	if err != nil {
		return 0, false
	}
	return zone, true
}

func defaultThermalCSV() string {
	if v, ok := os.LookupEnv("PROMO_THERMAL_CSV"); ok {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home + "/ops/thermal.csv"
}
