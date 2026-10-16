package status

// AudioSlot is one filled device slot of the audio block (#145): the slot
// number (1–5, what the SmartThings list and Telegram's buttons name), the
// device name the user put there, whether such a device is active now
// (present) and whether it is the default one (default). Empty slots are
// left out.
type AudioSlot struct {
	Slot    int    `json:"slot"`
	Name    string `json:"name"`
	Present bool   `json:"present"`
	Default bool   `json:"default"`
}

// DefaultSlot is the slot holding the default device, nil when none does.
func DefaultSlot(slots []AudioSlot) *AudioSlot {
	for i := range slots {
		if slots[i].Default {
			return &slots[i]
		}
	}
	return nil
}
