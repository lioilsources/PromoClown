package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	defaultHotC    = 93.0 // pause at or above
	defaultCoolC   = 87.0 // and stay paused until below
	defaultMaxWait = 10 * time.Minute
	// thermalStale is how old the last row may be before it is not believed.
	thermalStale = time.Minute
)

// errThermalWait ends a run: the box did not cool down, or could not be read,
// within the longest wait. What is still queued stays for the next run.
var errThermalWait = errors.New("thermal wait ran out")

type thermalGuard struct {
	path      string
	hot, cool float64
	maxWait   time.Duration
	poll      time.Duration
	now       func() time.Time
	log       io.Writer
}

func newThermalGuard(path string, hot, cool float64, maxWait time.Duration, log io.Writer) *thermalGuard {
	return &thermalGuard{path: path, hot: hot, cool: cool, maxWait: maxWait, poll: 15 * time.Second, now: time.Now, log: log}
}

// on reports whether there is a thermal log to watch.
func (g *thermalGuard) on() bool { return g != nil && g.path != "" }

// wait blocks while the box is hot: from the hot mark until the zone is back
// under the cool one. A missing or stale log blocks too, as it does for the
// other batches — a dead logger is no proof the box is cool — so a machine
// without one has to turn the guard off (--thermal-csv ""). After maxWait it
// gives up with errThermalWait.
func (g *thermalGuard) wait(ctx context.Context) error {
	if !g.on() {
		return nil
	}
	zone, ok := g.zone()
	if ok && zone < g.hot {
		return nil
	}
	if ok {
		fmt.Fprintf(g.log, "thermal pause: zone %.1f °C, waiting for under %.0f °C\n", zone, g.cool)
	} else {
		fmt.Fprintf(g.log, "thermal pause: no fresh reading in %s\n", g.path)
	}
	giveUp := time.After(g.maxWait)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-giveUp:
			if ok {
				return fmt.Errorf("%w: zone still %.1f °C after %s", errThermalWait, zone, g.maxWait)
			}
			return fmt.Errorf("%w: no fresh reading in %s after %s", errThermalWait, g.path, g.maxWait)
		case <-time.After(g.poll):
		}
		if zone, ok = g.zone(); ok && zone < g.cool {
			fmt.Fprintf(g.log, "thermal resume: zone %.1f °C\n", zone)
			return nil
		}
	}
}

// event appends a line to thermal-events.csv next to the thermal log, where
// every batch on the Spark leaves its start and end, so that a later look at
// the temperatures can tell who was heating. Only an existing file is written.
func (g *thermalGuard) event(note string) {
	if !g.on() {
		return
	}
	f, err := os.OpenFile(filepath.Join(filepath.Dir(g.path), "thermal-events.csv"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s.000,\"promo-tribute: %s\"\n", g.now().Format("2006/01/02 15:04:05"), note)
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
