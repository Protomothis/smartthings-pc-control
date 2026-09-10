package release

import (
	"context"
	"net/http"
	"net/http/httptest"
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
		{"v0.9.9", "v0.10.0", true}, // numeric, not lexicographic
		{"v1.0.0-rc1", "v1.0.0", false},
	}
	for _, c := range cases {
		if got := IsNewer(c.current, c.latest); got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestParseVersion(t *testing.T) {
	if p, ok := ParseVersion(" v1.2.3-rc1 "); !ok || p != [3]int{1, 2, 3} {
		t.Errorf("ParseVersion(v1.2.3-rc1) = %v, %v", p, ok)
	}
	for _, bad := range []string{"", "dev", "v1.2", "v1.2.x", "1.2.3.4"} {
		if _, ok := ParseVersion(bad); ok {
			t.Errorf("ParseVersion(%q) accepted", bad)
		}
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
			w.Write([]byte(`{"tag_name":"v0.4.0","html_url":"https://x/rel","assets":[{"name":"smartthings-pc-control.exe","browser_download_url":"https://x/a.exe","size":7}]}`))
		case "/bad":
			w.Write([]byte(`{not json`))
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
	if _, err := Fetch(context.Background(), nil, srv.URL+"/missing"); err == nil {
		t.Errorf("HTTP 404 succeeded, want error")
	}
	if _, err := Fetch(context.Background(), nil, srv.URL+"/bad"); err == nil {
		t.Errorf("invalid JSON succeeded, want error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Fetch(ctx, nil, srv.URL+"/ok"); err == nil {
		t.Errorf("cancelled context succeeded, want error")
	}
}
