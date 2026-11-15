package gui

// The SmartThings screen's toolkit-free logic (#70, #95, #96, #139): the
// form model of its two config fields (the allowed hubs and the WoL
// adapter), the WoL adapter choices and the diagnostic lines. The Fyne
// section (st_section.go) and the Gio page (ui/pages_smartthings.go) both
// build on it.

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// --- Pure form model (unit-tested) -----------------------------------------

// stFormState is the section's contents as plain values, independent of
// widgets, so the config round-trip and the dirty check are testable.
type stFormState struct {
	// Hubs is the edited allow list; empty means "any hub".
	Hubs []string
	// WoLMAC is the adapter the dropdown picked; empty means automatic.
	WoLMAC string
}

// stStateFromConfig is what the section shows for cfg. The hub list is
// copied so editing it never writes into the baseline.
func stStateFromConfig(cfg Config) stFormState {
	return stFormState{
		Hubs:   normalizeHubs(cfg.SmartThings.AllowedHubs),
		WoLMAC: cfg.SmartThings.WoLMAC,
	}
}

// normalizeHubs trims, drops blanks and de-duplicates the allow list.
// Never nil: the service reads a missing/null allowed_hubs as "keep the
// stored list", so clearing it has to send [].
func normalizeHubs(hubs []string) []string {
	out := []string{}
	for _, h := range hubs {
		if h = strings.TrimSpace(h); h != "" && !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	return out
}

// addHub returns the list with ip appended when it is not already on it.
func addHub(hubs []string, ip string) []string {
	out := normalizeHubs(hubs)
	ip = strings.TrimSpace(ip)
	if ip == "" || slices.Contains(out, ip) {
		return out
	}
	return append(out, ip)
}

// removeHub returns the list without ip.
func removeHub(hubs []string, ip string) []string {
	out := []string{}
	for _, h := range normalizeHubs(hubs) {
		if h != ip {
			out = append(out, h)
		}
	}
	return out
}

// applyTo returns base with the section's two fields written over it.
// Everything else — the rest of smartthings (the sharing tab's), port,
// secret, telegram… — is untouched.
func (s stFormState) applyTo(base Config) Config {
	cfg := base
	cfg.SmartThings.AllowedHubs = normalizeHubs(s.Hubs)
	cfg.SmartThings.WoLMAC = s.WoLMAC
	return cfg
}

// dirty reports whether saving would change base.
func (s stFormState) dirty(base Config) bool {
	st := base.SmartThings
	return s.WoLMAC != st.WoLMAC ||
		!slices.Equal(normalizeHubs(s.Hubs), normalizeHubs(st.AllowedHubs))
}

// --- WoL adapter labels (unit-tested) ---------------------------------------

// stWoLStateLabel is the trailing "WoL 켜짐 / 꺼짐 / 미지원" of a dropdown
// entry. "off" and "not supported" are different problems: the first the
// user can fix in the adapter's properties, the second they cannot.
func stWoLStateLabel(l Lang, enabled, capable bool) string {
	switch {
	case enabled:
		return T(l, "st.wol.on")
	case capable:
		return T(l, "st.wol.off")
	}
	return T(l, "st.wol.na")
}

// stWoLAutoOption is the dropdown's first entry, "자동 (이더넷 ·
// B4-2E-99-45-B4-F5)": automatic, with what automatic currently means
// spelled out so picking it is not a leap of faith.
func stWoLAutoOption(l Lang, auto *STWoLSelected) string {
	if auto == nil || auto.MAC == "" {
		return T(l, "st.wol.auto.none")
	}
	return fmt.Sprintf(T(l, "st.wol.auto"), auto.Name, auto.MAC)
}

// stWoLAdapterOption is one adapter's entry, "이더넷 · B4-2E-99-45-B4-F5 ·
// WoL 켜짐".
func stWoLAdapterOption(l Lang, a STWoLAdapter) string {
	return fmt.Sprintf(T(l, "st.wol.adapter"), a.Name, a.MAC, stWoLStateLabel(l, a.WoLEnabled, a.WoLCapable))
}

// stWoLOptions builds the dropdown from the service's picture: the labels
// and, in step with them, the wol_mac each one saves ("" for automatic).
// mac is the value currently in the form; when it names no adapter — the
// card was swapped, or config.json was edited by hand — it keeps an entry
// of its own so the dropdown never shows something other than what is
// saved.
func stWoLOptions(l Lang, info STWoLInfo, mac string) (labels, macs []string) {
	labels = []string{stWoLAutoOption(l, info.Auto)}
	macs = []string{""}
	found := false
	for _, a := range info.Adapters {
		if a.MAC == "" {
			continue
		}
		if strings.EqualFold(a.MAC, mac) {
			found = true
		}
		labels = append(labels, stWoLAdapterOption(l, a))
		macs = append(macs, a.MAC)
	}
	if mac != "" && !found {
		labels = append(labels, fmt.Sprintf(T(l, "st.wol.missing"), mac))
		macs = append(macs, mac)
	}
	return labels, macs
}

// stWoLPick resolves the form's wol_mac against the service's picture, the
// same way the service does: the adapter it names, or the automatic pick
// when it is empty or matches nothing. It is what the hint below the
// dropdown describes, so an unsaved choice is explained straight away.
func stWoLPick(info STWoLInfo, mac string) *STWoLSelected {
	if mac != "" {
		for _, a := range info.Adapters {
			if strings.EqualFold(a.MAC, mac) {
				return &STWoLSelected{
					Name: a.Name, MAC: a.MAC, IP: a.IP,
					WoLEnabled: a.WoLEnabled, WoLCapable: a.WoLCapable,
					Source: "manual",
				}
			}
		}
	}
	return info.Auto
}

// stWoLLine is the hint under the dropdown: whether this PC can actually
// be woken through the chosen adapter, naming it rather than talking about
// adapters in general.
func stWoLLine(l Lang, sel *STWoLSelected) string {
	if sel == nil || sel.MAC == "" {
		return T(l, "st.wol.none")
	}
	switch {
	case sel.WoLEnabled:
		return fmt.Sprintf(T(l, "st.wol.ready"), sel.Name)
	case sel.WoLCapable:
		return fmt.Sprintf(T(l, "st.wol.notready"), sel.Name)
	}
	return fmt.Sprintf(T(l, "st.wol.unsupported"), sel.Name)
}

// stMachineIDShort is the first 8 characters of the machine id
// ("58bff996"), which is what the Edge driver shows as the device model
// and what tells two PCs apart at a glance. A shorter id is used whole;
// the [복사] button always copies the full value.
func stMachineIDShort(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// stSearchLine is the discovery diagnostic: is the responder listening, is
// the firewall rule there, and did a search ever arrive. A responder that
// could not open a socket answers nothing, so that case replaces the whole
// line rather than adding to it — the rest would only be noise.
func stSearchLine(l Lang, s STSSDPState, now time.Time) string {
	if !s.Running {
		return T(l, "st.search.off")
	}
	firewall := "st.search.fw.missing"
	if s.FirewallRule {
		firewall = "st.search.fw.ok"
	}
	last := T(l, "st.search.none")
	if s.LastSearch != nil && s.LastSearch.IP != "" {
		last = fmt.Sprintf(T(l, "st.search.last"), s.LastSearch.IP, RelativeTime(l, s.LastSearch.At, now))
	}
	return strings.Join([]string{T(l, "st.search.on"), T(l, firewall), last}, " · ")
}

// stCommandPortLine is the command port's firewall rule (#139): the port
// the service checked at its last start and whether the rule allows it.
// savedPort is the port in the saved settings (0 = unknown); when it
// differs, the service has not restarted since the change, and the line
// says so. "" hides the line (a service that does not report the rule).
func stCommandPortLine(l Lang, c *STCommandPort, savedPort int) string {
	if c == nil {
		return ""
	}
	if !c.Checked {
		return T(l, "st.cmdport.checking")
	}
	key := "st.cmdport.missing"
	if c.FirewallRule {
		key = "st.cmdport.ok"
	}
	line := fmt.Sprintf(T(l, key), c.Port)
	if savedPort > 0 && savedPort != c.Port {
		line += " · " + fmt.Sprintf(T(l, "st.cmdport.pending"), savedPort)
	}
	return line
}

// --- for the Gio page (ui/pages_smartthings.go) ------------------------------

// STForm is the SmartThings page's fields as plain values: the allowed
// hubs (empty means any hub) and the WoL adapter's MAC ("" automatic).
type STForm struct {
	Hubs   []string
	WoLMAC string
}

// NewSTForm is what the page shows for cfg; the hub list is a copy.
func NewSTForm(cfg Config) STForm { return STForm(stStateFromConfig(cfg)) }

// ApplyTo writes the two fields over cfg and nothing else. The hub list is
// a new slice, never the baseline's.
func (f STForm) ApplyTo(cfg *Config) { *cfg = stFormState(f).applyTo(*cfg) }

// Dirty reports whether saving would change base.
func (f STForm) Dirty(base Config) bool { return stFormState(f).dirty(base) }

// AddHub is the list with ip appended unless it is already on it.
func AddHub(hubs []string, ip string) []string { return addHub(hubs, ip) }

// RemoveHub is the list without ip.
func RemoveHub(hubs []string, ip string) []string { return removeHub(hubs, ip) }

// NormalizeHubs trims, drops blanks and de-duplicates (never nil).
func NormalizeHubs(hubs []string) []string { return normalizeHubs(hubs) }

// STWoLOptions are the adapter choices: labels and, in step, the wol_mac
// each saves ("" automatic first; an unknown saved MAC keeps an entry).
func STWoLOptions(l Lang, info STWoLInfo, mac string) (labels, macs []string) {
	return stWoLOptions(l, info, mac)
}

// STWoLPick is the adapter mac resolves to, as the service resolves it.
func STWoLPick(info STWoLInfo, mac string) *STWoLSelected { return stWoLPick(info, mac) }

// STWoLLine says whether the PC can be woken through sel.
func STWoLLine(l Lang, sel *STWoLSelected) string { return stWoLLine(l, sel) }

// STWoLStateLabel is "WoL 켜짐 / 꺼짐 / 미지원".
func STWoLStateLabel(l Lang, enabled, capable bool) string {
	return stWoLStateLabel(l, enabled, capable)
}

// STMachineIDShort is the first 8 characters of the machine id.
func STMachineIDShort(id string) string { return stMachineIDShort(id) }

// STSearchLine is the discovery diagnostic in one line.
func STSearchLine(l Lang, s STSSDPState, now time.Time) string { return stSearchLine(l, s, now) }

// STCommandPortLine is the command port's firewall rule in one line ("" when
// the service does not report it).
func STCommandPortLine(l Lang, c *STCommandPort, savedPort int) string {
	return stCommandPortLine(l, c, savedPort)
}
