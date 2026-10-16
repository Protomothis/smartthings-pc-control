package status

import "github.com/Protomothis/smartthings-pc-control/useraction"

// Monitor is one entry of the status "monitors" block (#144): the reading
// `user-action monitor get` made, as it made it.
//
//	{"id":"m1","name":"DELL U2720Q","primary":true,"ddc":true,
//	 "brightness":40,"input":"hdmi1","inputs":["dp1","hdmi1"],"power":"on"}
type Monitor = useraction.Monitor
