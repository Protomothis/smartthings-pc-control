// Package release knows how to ask GitHub for the newest published release
// of smartthings-pc-control and how to compare release tags. It is shared
// by the GUI (update dialog / self-update) and the service (the
// system.update_available notification) and does no I/O beyond the HTTP
// request it is handed.
package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// API lists the project's releases, newest first. The app does not use
// /releases/latest: the Edge driver publishes its own `edge-v*` releases in
// the same repository, and when one of those is marked Latest the app gets a
// tag it cannot parse and never sees its own updates (#119). Newest picks the
// app release out of this list instead. 30 entries cover many months of app
// and driver releases together.
const API = "https://api.github.com/repos/Protomothis/smartthings-pc-control/releases?per_page=30"

// Page is the human-facing releases page, used when a release carries no
// html_url of its own. It is the list rather than /releases/latest for the
// same reason as API.
const Page = "https://github.com/Protomothis/smartthings-pc-control/releases"

// AssetName is the single binary attached to every release by
// .github/workflows/release.yml.
const AssetName = "smartthings-pc-control.exe"

// ErrNoRelease: the release list holds no published, non-prerelease app
// release (only drafts, prereleases, Edge driver releases or nothing).
var ErrNoRelease = errors.New("no published app release found")

// Asset is one file attached to a release.
type Asset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Size        int64  `json:"size"`
}

// Info is the subset of the GitHub release object we use.
type Info struct {
	TagName    string  `json:"tag_name"`
	HTMLURL    string  `json:"html_url"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

// Latest asks GitHub for the newest published app release (see Newest). A
// nil client uses http.DefaultClient; callers normally pass one with a
// timeout.
func Latest(ctx context.Context, client *http.Client) (*Info, error) {
	return Fetch(ctx, client, API)
}

// Fetch is Latest against an arbitrary release-list URL (tests point it at
// an httptest.Server). Any status other than 200 is an error, as is a list
// without an app release (ErrNoRelease).
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
	var list []Info
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	rel := Newest(list)
	if rel == nil {
		return nil, ErrNoRelease
	}
	return rel, nil
}

// stableTag is an app release tag with no suffix: v1.2.3, never
// v1.2.3-rc1 or edge-v1.2.3.
var stableTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// Newest returns the highest app release in list by version (not by date,
// so a hotfix for an older line published later does not win): the tag must
// be a plain vX.Y.Z and the release neither a draft nor a prerelease. nil
// when there is none.
func Newest(list []Info) *Info {
	var best *Info
	var bestV Version
	for i := range list {
		r := &list[i]
		if r.Draft || r.Prerelease || !stableTag.MatchString(r.TagName) {
			continue
		}
		v, ok := ParseVersion(r.TagName)
		if !ok {
			continue
		}
		if best == nil || v.Compare(bestV) > 0 {
			best, bestV = r, v
		}
	}
	return best
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

// Version is a parsed release tag: v1.2.3 or v1.2.3-rc4.
type Version struct {
	Major, Minor, Patch int
	// Pre is the pre-release suffix after the first "-" ("rc4"), "" for a
	// release.
	Pre string
}

// ParseVersion parses "v1.2.3" or "v1.2.3-rc4" (the leading v is optional).
// ok is false for non-release builds ("dev", "") so they never trigger
// update prompts, and for anything else that is not three numbers with an
// optional suffix of letters, digits, dots and dashes.
func ParseVersion(v string) (ver Version, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	core, pre, hasPre := strings.Cut(v, "-")
	if hasPre && !validPre(pre) {
		return Version{}, false
	}
	fields := strings.Split(core, ".")
	if len(fields) != 3 {
		return Version{}, false
	}
	var n [3]int
	for i, f := range fields {
		if f == "" || strings.Trim(f, "0123456789") != "" {
			return Version{}, false
		}
		x, err := strconv.Atoi(f)
		if err != nil {
			return Version{}, false
		}
		n[i] = x
	}
	return Version{Major: n[0], Minor: n[1], Patch: n[2], Pre: pre}, true
}

func validPre(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}

// String renders v as a tag: v1.2.3 or v1.2.3-rc4.
func (v Version) String() string {
	s := fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Compare returns -1, 0 or +1 as v is older than, equal to or newer than o.
// Numbers compare numerically; at equal numbers any pre-release is older
// than the release (v1.2.0-rc3 < v1.2.0, also for unknown suffixes), and
// two pre-releases compare by comparePre (rc2 < rc10, beta < rc).
func (v Version) Compare(o Version) int {
	for _, d := range [3]int{v.Major - o.Major, v.Minor - o.Minor, v.Patch - o.Patch} {
		switch {
		case d < 0:
			return -1
		case d > 0:
			return 1
		}
	}
	switch {
	case v.Pre == o.Pre:
		return 0
	case v.Pre == "":
		return 1
	case o.Pre == "":
		return -1
	}
	return comparePre(v.Pre, o.Pre)
}

// comparePre orders two non-empty pre-release suffixes. Each is split into
// runs of digits and of other characters ("rc10" -> "rc", 10; "rc.2" ->
// "rc", 2; dots and dashes only separate). Runs compare pairwise: numbers
// numerically, words case-insensitively, a number before a word (as in
// semver); when one is a prefix of the other the shorter is older.
func comparePre(a, b string) int {
	ta, tb := preTokens(a), preTokens(b)
	for i := 0; i < len(ta) && i < len(tb); i++ {
		x, y := ta[i], tb[i]
		xn, xNum := atoiDigits(x)
		yn, yNum := atoiDigits(y)
		switch {
		case xNum && yNum:
			if xn != yn {
				if xn < yn {
					return -1
				}
				return 1
			}
		case xNum:
			return -1
		case yNum:
			return 1
		default:
			if c := strings.Compare(strings.ToLower(x), strings.ToLower(y)); c != 0 {
				return c
			}
		}
	}
	switch {
	case len(ta) < len(tb):
		return -1
	case len(ta) > len(tb):
		return 1
	}
	return 0
}

func preTokens(s string) []string {
	var out []string
	start := -1
	digit := false
	flush := func(end int) {
		if start >= 0 {
			out = append(out, s[start:end])
			start = -1
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '.' || c == '-' {
			flush(i)
			continue
		}
		d := c >= '0' && c <= '9'
		if start >= 0 && d != digit {
			flush(i)
		}
		if start < 0 {
			start, digit = i, d
		}
	}
	flush(len(s))
	return out
}

// atoiDigits parses an all-digit token; ok is false for words and for
// numbers too large for an int (those then compare as words).
func atoiDigits(s string) (int, bool) {
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// Compare parses both tags and compares them (see Version.Compare). ok is
// false when either side fails ParseVersion.
func Compare(a, b string) (c int, ok bool) {
	va, ok := ParseVersion(a)
	if !ok {
		return 0, false
	}
	vb, ok := ParseVersion(b)
	if !ok {
		return 0, false
	}
	return va.Compare(vb), true
}

// IsNewer reports whether latest is a higher version than current, counting
// pre-releases as older than their release: an installed v1.2.0-rc4 sees
// v1.2.0 as newer. Either side failing ParseVersion yields false.
func IsNewer(current, latest string) bool {
	c, ok := Compare(latest, current)
	return ok && c > 0
}
