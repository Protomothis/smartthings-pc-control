package service

import (
	"testing"
	"time"
)

// A schedule outlives a service restart (#173): armed, it is in
// state.json; at the next start it comes back, unless its time passed.
func TestScheduleSavedAndRestored(t *testing.T) {
	path := statePath()
	defer scheduler.End("test", false)
	at := time.Now().Add(5 * 24 * time.Hour).Truncate(time.Minute)
	if err := scheduler.ScheduleAt("lock", at, originTelegram); err != nil {
		t.Fatal(err)
	}
	saved := loadState(path).Schedule
	if saved == nil || saved.Command != "lock" || saved.Origin != "telegram" || !saved.At.Equal(at) {
		t.Fatalf("state.json schedule = %+v", saved)
	}

	// A restart: the slot is empty, state.json still has it.
	scheduler.End("test", false)
	st := loadState(path)
	st.Schedule = saved
	if err := saveState(path, st); err != nil {
		t.Fatal(err)
	}
	restoreSchedule(path)
	cur, ok := scheduler.Current()
	if !ok || cur.Command != "lock" || cur.Origin != originTelegram || !cur.ExecuteAt.Equal(at) {
		t.Fatalf("restored = %+v, %v", cur, ok)
	}

	// Cancelled, it leaves state.json.
	scheduler.End("test", false)
	if s := loadState(path).Schedule; s != nil {
		t.Errorf("after cancel: %+v", s)
	}

	// One that was due while the service was down is dropped, not run.
	st = loadState(path)
	st.Schedule = &savedSchedule{Command: "lock", Origin: "ui", At: time.Now().Add(-time.Hour)}
	if err := saveState(path, st); err != nil {
		t.Fatal(err)
	}
	restoreSchedule(path)
	if _, ok := scheduler.Current(); ok {
		t.Error("a missed schedule was armed")
	}
	if s := loadState(path).Schedule; s != nil {
		t.Errorf("missed schedule kept: %+v", s)
	}

	// A remote grace deferral is never saved.
	if err := scheduler.Schedule("lock", time.Minute, originRemote); err != nil {
		t.Fatal(err)
	}
	if s := loadState(path).Schedule; s != nil {
		t.Errorf("grace deferral saved: %+v", s)
	}
}
