package webui

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
)

// POST /api/audio/slots/reset (#159) answers the slots the service saved;
// a failed save is a 500, and a service without the hook a 404.
func TestAudioSlotsReset(t *testing.T) {
	s := newTestServer(t, config.Config{Port: 5001})
	post := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.Routes(false).ServeHTTP(w, localRequest(s, httptest.NewRequest("POST", "/api/audio/slots/reset", nil)))
		return w
	}
	if w := post(); w.Code != http.StatusNotFound {
		t.Errorf("without the hook: %d", w.Code)
	}

	calls := 0
	s.d.ResetAudioSlots = func() (config.AudioConfig, error) {
		calls++
		return config.AudioConfig{Outputs: []string{"스피커"}, Inputs: []string{}}, nil
	}
	w := post()
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"outputs":["스피커"],"inputs":[]}` || calls != 1 {
		t.Errorf("reset: %d %s (calls %d)", w.Code, w.Body.String(), calls)
	}

	s.d.ResetAudioSlots = func() (config.AudioConfig, error) { return config.AudioConfig{}, errors.New("disk full") }
	if w := post(); w.Code != http.StatusInternalServerError {
		t.Errorf("failed save: %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	s.Routes(false).ServeHTTP(w, localRequest(s, httptest.NewRequest("GET", "/api/audio/slots/reset", nil)))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", w.Code)
	}
}
