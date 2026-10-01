package service

// The SmartThings surface (service/stapi): /st/v1 on the command port, the
// hub pushes and the SSDP responder, wired to the service.

import (
	"net/http"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/stapi"
)

// stSrv is the service's /st/v1 surface.
var stSrv = stapi.New(stapi.Deps{
	Config:   getConfig,
	Version:  func() string { return Version },
	Emit:     func(cat, kind string, fields map[string]string) { emit(cat, kind, fields) },
	Status:   sources,
	Commands: stCommands{},
	Awake:    awakeControl{},
	Media:    mediaControl{},
	Presets:  presetControl{origin: "smartthings"},
	Notify:   pcNotifier{},
})

// registerSTRoutes mounts the /st/v1 tree on the command server's mux. A
// more specific pattern wins over "/", so the legacy /{secret}/{command}
// handler still sees everything else.
func registerSTRoutes(mux *http.ServeMux) {
	mux.Handle("/st/v1/", stSrv.Handler())
}

// stCommands is the catalogue and the schedule slot on behalf of
// SmartThings: origin "smartthings", one path with every other caller
// (dispatchCommand).
type stCommands struct{}

func (stCommands) Known(name string) bool {
	_, ok := Commands[name]
	return ok
}

func (stCommands) Dispatch(name, from string, mode stapi.Mode) time.Duration {
	dm := dispatchDefault
	switch mode {
	case stapi.ModeImmediate:
		dm = dispatchImmediate
	case stapi.ModeGrace:
		dm = dispatchGrace
	}
	deferred, _ := dispatchCommand(name, from, originSmartThings, dm)
	return deferred
}

func (stCommands) Record(name, from string) { recordCommand(name, from, originSmartThings) }

func (stCommands) Schedule(name string, delay time.Duration) error {
	return setSchedule(name, delay, originSmartThings)
}

func (stCommands) Cancel(by string) bool { return cancelScheduleBy(by) }
