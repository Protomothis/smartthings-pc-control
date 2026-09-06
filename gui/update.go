package gui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const releasesAPI = "https://api.github.com/repos/Protomothis/smartthings-pc-control/releases/latest"
const releasesPage = "https://github.com/Protomothis/smartthings-pc-control/releases/latest"

type releaseInfo struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

// checkLatestRelease asks GitHub for the newest published release.
func checkLatestRelease() (*releaseInfo, error) {
	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequest("GET", releasesAPI, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API: HTTP %d", resp.StatusCode)
	}
	var rel releaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// parseVersion turns "v1.2.3" into comparable parts. ok is false for
// non-release builds ("dev") so they never trigger update prompts.
func parseVersion(v string) (parts [3]int, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	fields := strings.SplitN(v, ".", 3)
	if len(fields) != 3 {
		return parts, false
	}
	for i, f := range fields {
		// Tolerate suffixes like "3-rc1" on the last field.
		f = strings.SplitN(f, "-", 2)[0]
		n, err := strconv.Atoi(f)
		if err != nil {
			return parts, false
		}
		parts[i] = n
	}
	return parts, true
}

// isNewer reports whether latest is a higher release version than current.
func isNewer(current, latest string) bool {
	c, ok := parseVersion(current)
	if !ok {
		return false
	}
	l, ok := parseVersion(latest)
	if !ok {
		return false
	}
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}
