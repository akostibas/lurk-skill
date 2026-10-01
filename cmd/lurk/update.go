package main

import (
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	// The web redirect, not the API: anonymous API calls share a 60/hour limit
	// per IP that a busy network exhausts. It redirects to …/releases/tag/<tag>.
	releaseURL    = "https://github.com/akostibas/lurk-skill/releases/latest"
	checkInterval = 7 * 24 * time.Hour
)

// updateNotice returns a one-line upgrade hint, or "" when current. GitHub is
// asked at most once a week; the answer is cached so the hint repeats on every
// run until the user upgrades, and an offline check fails silently.
func updateNotice(current string) string {
	if parseVersion(current) == nil {
		return "" // dev or dirty build
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	cache := filepath.Join(dir, "lurk", "latest-release")
	latest := ""
	if b, err := os.ReadFile(cache); err == nil {
		latest = strings.TrimSpace(string(b))
	}
	if fi, err := os.Stat(cache); err != nil || time.Since(fi.ModTime()) > checkInterval {
		if tag := fetchLatest(); tag != "" {
			latest = tag
		}
		// Written even on failure so an offline Mac doesn't retry every run.
		os.MkdirAll(filepath.Dir(cache), 0o755)
		os.WriteFile(cache, []byte(latest+"\n"), 0o644)
	}
	if !newer(latest, current) {
		return ""
	}
	how := "run update.sh in the lurk skill folder"
	if exe, err := os.Executable(); err == nil {
		if u := filepath.Join(filepath.Dir(exe), "update.sh"); fileExists(u) {
			how = "run " + u
		}
	}
	return fmt.Sprintf("lurk %s is available (you have %s) — to upgrade, %s", latest, current, how)
}

func fetchLatest() string {
	c := http.Client{
		Timeout:       2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := c.Get(releaseURL)
	if err != nil {
		return ""
	}
	resp.Body.Close()
	tag := path.Base(resp.Header.Get("Location"))
	if parseVersion(tag) == nil {
		return ""
	}
	return tag
}

// parseVersion reads an exact vMAJOR.MINOR.PATCH tag; anything else is nil.
func parseVersion(v string) []int {
	var a, b, c int
	var rest string
	if n, _ := fmt.Sscanf(v+" x", "v%d.%d.%d %s", &a, &b, &c, &rest); n != 4 || rest != "x" {
		return nil
	}
	return []int{a, b, c}
}

func newer(latest, current string) bool {
	l, c := parseVersion(latest), parseVersion(current)
	if l == nil || c == nil {
		return false
	}
	for i := range l {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
