//go:build windows

package windowsjob

import (
	"sync"
	"testing"
)

func TestJobConcurrentCloseAndTerminate(t *testing.T) {
	job, err := NewJob()
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if err := job.Terminate(); err != nil {
				t.Errorf("terminate: %v", err)
			}
		})
		wg.Go(job.Close)
	}
	wg.Wait()
}
