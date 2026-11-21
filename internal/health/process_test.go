package health

import (
	"os"
	"testing"
	"time"
)

func TestReadProcess(t *testing.T) {
	p, err := ReadProcess()
	if err != nil {
		t.Fatal(err)
	}
	if int(p.PID) != os.Getpid() || p.Started.IsZero() || p.Started.After(time.Now()) {
		t.Errorf("pid %d started %v", p.PID, p.Started)
	}
	if p.WorkingSet == 0 || p.Private == 0 || p.Threads == 0 || p.Handles == 0 || p.Goroutines == 0 {
		t.Errorf("%+v", p)
	}
}
