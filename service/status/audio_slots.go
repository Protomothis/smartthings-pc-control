package status

// AudioSlot is one filled device slot of the audio block (#145, #159): the
// slot number (1–5, what a SmartThings routine compares against), the
// device name the service saved there when it first saw the device,
// whether such a device is active now (present) and whether it is the
// default one (default). Empty slots are left out.
type AudioSlot struct {
	Slot    int    `json:"slot"`
	Name    string `json:"name"`
	Present bool   `json:"present"`
	Default bool   `json:"default"`
}
