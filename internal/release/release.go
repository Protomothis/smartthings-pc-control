// Package release knows how to ask GitHub for the newest published release
// of smartthings-pc-control and how to compare release tags. It is shared
// by the GUI (update dialog / self-update) and the service (the
// system.update_available notification) and does no I/O beyond the HTTP
// request it is handed.
package release

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// API is the GitHub "latest release" endpoint for this project.
const API = "https://api.github.com/repos/Protomothis/smartthings-pc-control/releases/latest"

// Page is the human-facing releases page, used when a release carries no
// html_url of its own.
const Page = "https://github.com/Protomothis/smartthings-pc-control/releases/latest"

// AssetName is the single binary attached to every release by
// .github/workflows/release.yml.
const AssetName = "smartthings-pc-control.exe"

// Asset is one file attached to a release.
type Asset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Size        int64  `json:"size"`
}

// Info is the subset of the GitHub release object we use.
type Info struct {
	TagName string  `json:"tag_name"`
	HTMLURL string  `json:"html_url"`
	Assets  []Asset `json:"assets"`
}

// Latest asks GitHub for the newest published release. A nil client uses
// http.DefaultClient; callers normally pass one with a timeout.
func Latest(ctx context.Context, client *http.Client) (*Info, error) {
	return Fetch(ctx, client, API)
}

// Fetch is Latest against an arbitrary URL (tests point it at an
// httptest.Server).
func Fetch(ctx context.Context, client *http.Client, url string) (*Info, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
	var rel Info
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// ExeAsset returns the download URL of the release's exe asset, or "" when
// the release carries no asset we know how to install.
func ExeAsset(rel *Info) string {
	if rel == nil {
		return ""
	}
	for _, a := range rel.Assets {
		if strings.EqualFold(a.Name, AssetName) && a.DownloadURL != "" {
			return a.DownloadURL
		}
	}
	return ""
}

// ParseVersion turns "v1.2.3" into comparable parts. ok is false for
// non-release builds ("dev", "") so they never trigger update prompts.
func ParseVersion(v string) (parts [3]int, ok bool) {
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

// IsNewer reports whether latest is a higher release version than current.
// Either side failing ParseVersion yields false.
func IsNewer(current, latest string) bool {
	c, ok := ParseVersion(current)
	if !ok {
		return false
	}
	l, ok := ParseVersion(latest)
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
