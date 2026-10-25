package main

// Inbound media cache sweeper.
//
// The bridge auto-downloads every inbound media file into store/<chat_jid>/ so
// later MCP tool calls can read it. On a small persistent volume (Railway,
// 500MB) that filled the disk within a day — WhatsApp status updates alone
// were ~250MB — and once SQLite hit "database or disk is full" the bridge
// silently stopped storing messages *and* Signal sessions.
//
// Two guards:
//   - status@broadcast media is never auto-downloaded (see shouldAutoDownload).
//   - A periodic sweep deletes cached media older than MEDIA_CACHE_TTL_HOURS
//     (default 48h) and, if the cache is still over MEDIA_CACHE_MAX_MB
//     (default 150MB), evicts oldest-first until under the cap.
//
// Only regular files inside store/<chat dir>/ are touched; the SQLite
// databases, tokens.json and anything at the store root are never removed.

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const statusBroadcastJID = "status@broadcast"

func shouldAutoDownload(chatJID string) bool {
	return chatJID != statusBroadcastJID
}

type cachedMedia struct {
	path    string
	size    int64
	modTime time.Time
}

func envInt64(name string, def int64) int64 {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			return n
		}
	}
	return def
}

// listCachedMedia returns every regular file under storeDir/<subdir>/ (one
// level down). Files at the store root (databases, tokens) are excluded.
func listCachedMedia(storeDir string) ([]cachedMedia, error) {
	entries, err := os.ReadDir(storeDir)
	if err != nil {
		return nil, err
	}
	var out []cachedMedia
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "lost+found" {
			continue
		}
		dir := filepath.Join(storeDir, e.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if !f.Type().IsRegular() {
				continue
			}
			info, err := f.Info()
			if err != nil {
				continue
			}
			out = append(out, cachedMedia{
				path:    filepath.Join(dir, f.Name()),
				size:    info.Size(),
				modTime: info.ModTime(),
			})
		}
	}
	return out, nil
}

// sweepMediaCache applies the TTL and size cap. Returns files removed and
// bytes freed.
func sweepMediaCache(storeDir string, ttl time.Duration, maxBytes int64, now time.Time) (int, int64, error) {
	files, err := listCachedMedia(storeDir)
	if err != nil {
		return 0, 0, err
	}
	removed, freed := 0, int64(0)
	var kept []cachedMedia
	var total int64
	for _, f := range files {
		if ttl > 0 && now.Sub(f.modTime) > ttl {
			if os.Remove(f.path) == nil {
				removed++
				freed += f.size
			}
			continue
		}
		kept = append(kept, f)
		total += f.size
	}
	if maxBytes > 0 && total > maxBytes {
		sort.Slice(kept, func(i, j int) bool { return kept[i].modTime.Before(kept[j].modTime) })
		for _, f := range kept {
			if total <= maxBytes {
				break
			}
			if os.Remove(f.path) == nil {
				removed++
				freed += f.size
				total -= f.size
			}
		}
	}
	return removed, freed, nil
}

// startMediaCacheSweeper runs one sweep immediately and then hourly.
func startMediaCacheSweeper(storeDir string, logf func(string, ...interface{})) {
	ttl := time.Duration(envInt64("MEDIA_CACHE_TTL_HOURS", 48)) * time.Hour
	maxBytes := envInt64("MEDIA_CACHE_MAX_MB", 150) * 1024 * 1024
	run := func() {
		removed, freed, err := sweepMediaCache(storeDir, ttl, maxBytes, time.Now())
		if err != nil {
			logf("media cache sweep failed: %v", err)
			return
		}
		if removed > 0 {
			logf("media cache sweep: removed %d files, freed %.1f MB (ttl=%s cap=%dMB)", removed, float64(freed)/1024/1024, ttl, maxBytes/1024/1024)
		}
	}
	go func() {
		run()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			run()
		}
	}()
}
