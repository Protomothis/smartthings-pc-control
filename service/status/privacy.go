package status

// Privacy is the status "privacy" block (#143, docs/design/v1.3-sensors.md),
// also the privacy.changed push data. It is in the status only while
// privacy.enabled is on (nil otherwise, and the key is left out).
//
// Camera and Microphone are "in_use", "idle" or "unknown" (nobody logged
// in, the user's hive not loaded, a registry error, or no scan yet). Since
// is RFC3339 (local offset) or "": while something is in use, when the
// earliest use still going on began; otherwise when the last use ended;
// "" when neither is known. Which app uses a device is never reported.
type Privacy struct {
	Camera     string `json:"camera"`
	Microphone string `json:"microphone"`
	Since      string `json:"since"`
}
