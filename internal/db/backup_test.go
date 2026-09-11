package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestBackup(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	conn, err := Open(filepath.Join(dir, "promo.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	now := FormatTime(time.Now())
	if _, err := New(conn).UpsertProject(ctx, UpsertProjectParams{Slug: "kirian", Name: "Kirian", Status: "active", Now: now}); err != nil {
		t.Fatal(err)
	}

	backups := filepath.Join(dir, "backups")
	start := time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)
	var last string
	for i := 0; i < 3; i++ {
		if last, err = Backup(ctx, conn, backups, 2, start.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := filepath.Glob(filepath.Join(backups, "promo-*.db"))
	if len(files) != 2 || files[1] != last {
		t.Fatalf("kept %v, last %s", files, last)
	}

	restored, err := Open(last)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	p, err := New(restored).GetProjectBySlug(ctx, "kirian")
	if err != nil || p.Name != "Kirian" {
		t.Fatalf("restored project = %+v, %v", p, err)
	}
}
