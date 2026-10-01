package release

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"v0.3.1", "v0.3.2", true},
		{"v0.3.2", "v0.3.2", false},
		{"v0.3.2", "v0.3.1", false},
		{"v0.3.2", "v0.4.0", true},
		{"v0.3.2", "v1.0.0", true},
		{"dev", "v9.9.9", false}, // dev builds never prompt
		{"", "v9.9.9", false},
		{"v0.3.2", "garbage", false},
		{"v0.3.2", "edge-v1.0.1", false}, // Edge driver tag (#119)
		{"v0.9.9", "v0.10.0", true},      // numeric, not lexicographic
		// Pre-releases are older than their release (#119): an rc install
		// is offered the final version, never the other way round.
		{"v1.0.0-rc1", "v1.0.0", true},
		{"v1.2.0-rc4", "v1.2.0", true},
		{"v1.2.0", "v1.2.0-rc4", false},
		{"v1.2.0-rc2", "v1.2.0-rc10", true},
		{"v1.2.0-rc10", "v1.2.0-rc2", false},
		{"v1.2.0-rc4", "v1.2.0-rc4", false},
		{"v1.2.0-rc4", "v1.1.2", false},
		{"v1.2.0-rc4", "v1.2.1", true},
		{"v1.2.0-dirty", "v1.2.0", true}, // unknown suffix < release
		{"v1.2.0", "v1.2.0-dirty", false},
	}
	for _, c := range cases {
		if got := IsNewer(c.current, c.latest); got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.2.0", "v1.2.0", 0},
		{"1.2.0", "v1.2.0", 0},
		{"v1.2.0-rc3", "v1.2.0", -1},
		{"v1.2.0", "v1.2.0-rc3", 1},
		{"v1.2.0-rc2", "v1.2.0-rc10", -1},
		{"v1.2.0-rc10", "v1.2.0-rc2", 1},
		{"v1.2.0-rc.2", "v1.2.0-rc.10", -1},
		{"v1.2.0-rc2", "v1.2.0-rc.2", 0}, // separators only separate
		{"v1.2.0-RC2", "v1.2.0-rc2", 0},
		{"v1.2.0-alpha", "v1.2.0-beta", -1},
		{"v1.2.0-beta3", "v1.2.0-rc1", -1},
		{"v1.2.0-rc", "v1.2.0-rc1", -1},
		{"v1.2.0-1", "v1.2.0-rc1", -1}, // number before word, as in semver
		{"v1.2.0-foo", "v1.2.0", -1},
		{"v1.2.0-rc9", "v1.1.9", 1}, // numbers decide before the suffix
		{"v1.10.0", "v1.9.9", 1},
		{"v2.0.0-rc1", "v1.99.99", 1},
	}
	for _, c := range cases {
		got, ok := Compare(c.a, c.b)
		if !ok || got != c.want {
			t.Errorf("Compare(%q, %q) = %d, %v, want %d", c.a, c.b, got, ok, c.want)
		}
	}
	for _, bad := range [][2]string{{"dev", "v1.0.0"}, {"v1.0.0", ""}, {"edge-v1.0.1", "v1.0.0"}} {
		if _, ok := Compare(bad[0], bad[1]); ok {
			t.Errorf("Compare(%q, %q) accepted", bad[0], bad[1])
		}
	}
}

func TestParseVersion(t *testing.T) {
	good := map[string]Version{
		"v1.2.3":       {1, 2, 3, ""},
		" v1.2.3-rc1 ": {1, 2, 3, "rc1"},
		"1.2.3":        {1, 2, 3, ""},
		"v0.10.0":      {0, 10, 0, ""},
		"v1.2.0-rc.10": {1, 2, 0, "rc.10"},
		"v1.2.0-rc-1":  {1, 2, 0, "rc-1"},
	}
	for in, want := range good {
		if got, ok := ParseVersion(in); !ok || got != want {
			t.Errorf("ParseVersion(%q) = %+v, %v, want %+v", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "dev", "v1.2", "v1.2.x", "1.2.3.4", "v1.2.3-", "v1.2.3-rc 1",
		"v1.2.3+build", "v+1.2.3", "v1.-2.3", "edge-v1.0.1", "ci-abc123", "vv1.2.3"} {
		if v, ok := ParseVersion(bad); ok {
			t.Errorf("ParseVersion(%q) accepted as %+v", bad, v)
		}
	}
	if s := (Version{1, 2, 0, "rc4"}).String(); s != "v1.2.0-rc4" {
		t.Errorf("String = %q", s)
	}
	if s := (Version{1, 2, 0, ""}).String(); s != "v1.2.0" {
		t.Errorf("String = %q", s)
	}
}

func TestNewest(t *testing.T) {
	list := []Info{
		{TagName: "edge-v1.0.1", HTMLURL: "https://x/edge-v1.0.1"},    // the Latest of #119
		{TagName: "v1.3.0-rc1", HTMLURL: "https://x/v1.3.0-rc1"},      // suffix, even unflagged
		{TagName: "v1.2.1", HTMLURL: "https://x/v1.2.1", Draft: true}, // draft
		{TagName: "v1.2.5", HTMLURL: "https://x/v1.2.5", Prerelease: true},
		{TagName: "v1.1.3", HTMLURL: "https://x/v1.1.3"}, // newest by date, older line
		{TagName: "v1.2.0", HTMLURL: "https://x/v1.2.0"},
		{TagName: "v1.10.0x", HTMLURL: "https://x/bad"},
		{TagName: "v1.1.2", HTMLURL: "https://x/v1.1.2"},
	}
	got := Newest(list)
	if got == nil || got.TagName != "v1.2.0" || got.HTMLURL != "https://x/v1.2.0" {
		t.Fatalf("Newest = %+v, want v1.2.0", got)
	}
	if got := Newest([]Info{{TagName: "edge-v1.0.1"}, {TagName: "v1.2.0-rc1"}, {TagName: "v1.2.0", Draft: true}}); got != nil {
		t.Errorf("Newest without an app release = %+v, want nil", got)
	}
	if got := Newest(nil); got != nil {
		t.Errorf("Newest(nil) = %+v", got)
	}
}

func TestExeAsset(t *testing.T) {
	rel := &Info{TagName: "v0.3.3", Assets: []Asset{
		{Name: "checksums.txt", DownloadURL: "https://x/checksums.txt"},
		{Name: "SmartThings-PC-Control.EXE", DownloadURL: "https://x/asset.exe"},
	}}
	if got := ExeAsset(rel); got != "https://x/asset.exe" {
		t.Errorf("ExeAsset = %q, want the exe asset (case-insensitive)", got)
	}
	if got := ExeAsset(&Info{Assets: []Asset{{Name: "other.exe", DownloadURL: "u"}}}); got != "" {
		t.Errorf("ExeAsset picked unrelated asset %q", got)
	}
	if got := ExeAsset(&Info{Assets: []Asset{{Name: AssetName}}}); got != "" {
		t.Errorf("ExeAsset returned asset without URL: %q", got)
	}
	if got := ExeAsset(nil); got != "" {
		t.Errorf("ExeAsset(nil) = %q", got)
	}
}

func TestFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("Accept header = %q", r.Header.Get("Accept"))
		}
		switch r.URL.Path {
		case "/ok":
			// GitHub's order: newest first, here with the Edge release on top.
			w.Write([]byte(`[
				{"tag_name":"edge-v1.0.1","html_url":"https://x/edge","assets":[]},
				{"tag_name":"v0.5.0-rc1","html_url":"https://x/rc","prerelease":true,"assets":[]},
				{"tag_name":"v0.4.0","html_url":"https://x/rel","assets":[{"name":"smartthings-pc-control.exe","browser_download_url":"https://x/a.exe","size":7}]},
				{"tag_name":"v0.3.9","html_url":"https://x/old","assets":[]}
			]`))
		case "/none":
			w.Write([]byte(`[{"tag_name":"edge-v1.0.1","assets":[]}]`))
		case "/object":
			// The old /releases/latest shape is not a list.
			w.Write([]byte(`{"tag_name":"v0.4.0"}`))
		case "/bad":
			w.Write([]byte(`{not json`))
		case "/limited":
			http.Error(w, "rate limited", http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	rel, err := Fetch(context.Background(), nil, srv.URL+"/ok")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if rel.TagName != "v0.4.0" || rel.HTMLURL != "https://x/rel" || ExeAsset(rel) != "https://x/a.exe" || rel.Assets[0].Size != 7 {
		t.Errorf("Fetch decoded %+v", rel)
	}
	if _, err := Fetch(context.Background(), nil, srv.URL+"/none"); !errors.Is(err, ErrNoRelease) {
		t.Errorf("list without an app release: err = %v, want ErrNoRelease", err)
	}
	for _, path := range []string{"/missing", "/limited", "/bad", "/object"} {
		if _, err := Fetch(context.Background(), nil, srv.URL+path); err == nil {
			t.Errorf("%s succeeded, want error", path)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Fetch(ctx, nil, srv.URL+"/ok"); err == nil {
		t.Errorf("cancelled context succeeded, want error")
	}
}

// API and Page must not use /releases/latest: GitHub's Latest can be an
// Edge driver release (#119).
func TestAPIListsReleases(t *testing.T) {
	if !strings.HasSuffix(API, "/releases?per_page=30") || strings.Contains(API, "latest") || strings.Contains(Page, "latest") {
		t.Errorf("API = %q, Page = %q", API, Page)
	}
}
