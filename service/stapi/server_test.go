package stapi

import "testing"

func TestDriverVersionOf(t *testing.T) {
	cases := map[string]string{
		DriverAgent + "/1.0.0": "1.0.0",
		"curl/8.4.0":           "curl/8.4.0",
		"":                     "",
	}
	for ua, want := range cases {
		if got := driverVersionOf(ua); got != want {
			t.Errorf("driverVersionOf(%q) = %q, want %q", ua, got, want)
		}
	}
}

func TestSTHubAllowedMatching(t *testing.T) {
	hubs := []string{" 192.168.1.20 ", ""}
	if !hubAllowed(hubs, "192.168.1.20") {
		t.Error("a padded allow-list entry must still match")
	}
	if hubAllowed(hubs, "192.168.1.21") {
		t.Error("an unlisted source matched")
	}
	if !hubAllowed([]string{"192.168.1.20"}, "::ffff:192.168.1.20") {
		t.Error("the IPv4-mapped form of an allowed hub must match")
	}
}
