package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeCached(t *testing.T, dir, name string, size int, age time.Duration, now time.Time) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, make([]byte, size), 0644); err != nil {
		t.Fatal(err)
	}
	mt := now.Add(-age)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestShouldAutoDownloadSkipsStatus(t *testing.T) {
	if shouldAutoDownload(statusBroadcastJID) {
		t.Fatal("status@broadcast must not be auto-downloaded")
	}
	if !shouldAutoDownload("120363408685406959@g.us") {
		t.Fatal("group media should be auto-downloaded")
	}
}

func TestSweepMediaCacheTTLAndCap(t *testing.T) {
	store := t.TempDir()
	now := time.Now()
	chat := filepath.Join(store, "123@g.us")
	if err := os.MkdirAll(chat, 0755); err != nil {
		t.Fatal(err)
	}
	old := writeCached(t, chat, "old.jpg", 10, 72*time.Hour, now)
	mid := writeCached(t, chat, "mid.jpg", 100, 10*time.Hour, now)
	fresh := writeCached(t, chat, "fresh.jpg", 100, time.Hour, now)
	// Root-level files must never be touched.
	db := filepath.Join(store, "messages.db")
	if err := os.WriteFile(db, make([]byte, 5), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(db, now.Add(-200*time.Hour), now.Add(-200*time.Hour)); err != nil {
		t.Fatal(err)
	}

	removed, freed, err := sweepMediaCache(store, 48*time.Hour, 150, now)
	if err != nil {
		t.Fatal(err)
	}
	// old.jpg by TTL; then total=200 > cap 150 → evict oldest remaining (mid.jpg).
	if removed != 2 || freed != 110 {
		t.Fatalf("removed=%d freed=%d, want 2/110", removed, freed)
	}
	for _, p := range []string{old, mid} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s should have been removed", p)
		}
	}
	for _, p := range []string{fresh, db} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s should have been kept: %v", p, err)
		}
	}
}
