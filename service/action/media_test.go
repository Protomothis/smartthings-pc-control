package action

import (
	"errors"
	"reflect"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

func intp(n int) *int { return &n }

func TestMediaCommandArgs(t *testing.T) {
	ok := []struct {
		name  string
		value *int
		want  []string
	}{
		{"volume", intp(0), []string{"audio", "set", "0"}},
		{"volume", intp(30), []string{"audio", "set", "30"}},
		{"volume", intp(100), []string{"audio", "set", "100"}},
		{"volumeup", nil, []string{"audio", "step", "+5"}},
		{"volumeup", intp(1), []string{"audio", "step", "+1"}},
		{"volumedown", nil, []string{"audio", "step", "-5"}},
		{"volumedown", intp(100), []string{"audio", "step", "-100"}},
		{"mute", nil, []string{"audio", "mute", "on"}},
		// mute and unmute take no value; one sent anyway is ignored.
		{"unmute", intp(7), []string{"audio", "mute", "off"}},
		{"playpause", nil, []string{"media", "playpause"}},
		{"play", nil, []string{"media", "play"}},
		{"pause", nil, []string{"media", "pause"}},
		{"stop", nil, []string{"media", "stop"}},
		{"next", nil, []string{"media", "next"}},
		{"prev", nil, []string{"media", "prev"}},
	}
	for _, c := range ok {
		got, err := MediaArgs(c.name, c.value)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s %v: %q, %v; want %q", c.name, c.value, got, err, c.want)
			continue
		}
		// Whatever is built must pass the subcommand's own parser, or
		// runUserAction would refuse it.
		if _, err := useraction.Parse(got); err != nil {
			t.Errorf("%s: %q does not parse: %v", c.name, got, err)
		}
	}
	bad := []struct {
		name  string
		value *int
	}{
		{"volume", nil}, {"volume", intp(-1)}, {"volume", intp(101)},
		{"volumeup", intp(0)}, {"volumeup", intp(101)}, {"volumedown", intp(-5)},
	}
	for _, c := range bad {
		var ve *ValueError
		if _, err := MediaArgs(c.name, c.value); !errors.As(err, &ve) {
			t.Errorf("%s %v: err = %v, want a value error", c.name, *valueOr(c.value), err)
		}
	}
	// Every name the handler dispatches has arguments.
	for name := range mediaKinds {
		if _, err := MediaArgs(name, intp(5)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func valueOr(p *int) *int {
	if p == nil {
		return intp(-999)
	}
	return p
}
