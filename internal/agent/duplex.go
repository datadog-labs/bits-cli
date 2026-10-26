package agent

import (
	"context"
	"time"
)

// driveDuplex keeps the transport off the actor. Each incoming fact is applied
// on the actor, so another user turn can run after this root releases its slot.
func (t *turn) driveDuplex() error {
	t.ctx, t.cancel = context.WithCancel(t.parent)
	t.handlers, t.stopHandlers = context.WithCancel(t.ctx)
	t.byID = make(map[string]*clientCall)
	t.results = make(chan toolDone, maxConcurrentTools)
	t.slots = make(chan struct{}, maxConcurrentTools)
	t.e.do(func() { t.e.turn = t })
	events, done := t.startExchange()
	defer func() {
		t.cancel()
		if done != nil {
			timer := time.NewTimer(30 * time.Second)
			defer timer.Stop()
			select {
			case result := <-done:
				t.e.do(func() {
					if t.transcript == t.e.transcript {
						t.withTranscript(func() { t.adopt(result.conversation) })
					}
				})
			case <-timer.C:
			}
		}
		t.e.do(func() {
			if t.e.turn == t {
				t.e.turn = nil
			}
		})
	}()
	apply := func(fn func() error) error {
		var err error
		t.e.do(func() {
			if t.transcript != t.e.transcript {
				err = context.Canceled
				return
			}
			t.withTranscript(func() { err = fn() })
		})
		return err
	}
	for {
		select {
		case event := <-events:
			if err := apply(func() error { return t.apply(event) }); err != nil {
				return err
			}
		case result := <-done:
			done = nil
			return apply(func() error {
				t.ended = true
				t.adopt(result.conversation)
				if result.err == nil {
					t.acknowledge()
				}
				t.stopCalls(nil, nil)
				return result.err
			})
		case result := <-t.results:
			if err := apply(func() error { return t.acceptToolResult(result) }); err != nil {
				return err
			}
		case <-t.ctx.Done():
			return t.ctx.Err()
		}
	}
}

// withTranscript scopes block ownership while different runs share the actor.
func (t *turn) withTranscript(fn func()) {
	previous := t.e.transcript.run
	t.e.transcript.run = t.run
	defer func() {
		if t.e.busy {
			t.e.transcript.run = previous
		} else {
			t.e.transcript.run = 0
		}
	}()
	fn()
}

func (t *turn) acceptToolResult(finished toolDone) error {
	c := finished.call
	if resolved(c) {
		return nil
	}
	c.cancel()
	switch {
	case finished.err == nil:
		t.resolve(c, finished.result)
	case t.stopRequested || t.denied:
		t.resolve(c, toolFailedResult(finished.err))
	case t.parent.Err() != nil:
		return t.parent.Err()
	default:
		t.ended = true
		t.stopCalls(c, finished.err)
		return finished.err
	}
	return nil
}

// stopBackground detaches work from a conversation being replaced. Late events
// cannot mutate the new transcript; workers still finish their transport cleanup.
func (e *Engine) stopBackground() {
	for _, t := range e.background {
		t.cancel()
	}
}

// StopBackground cancels outstanding runs and waits for their bounded transport
// cleanup before a caller discards credentials. It does not close the engine.
func (e *Engine) StopBackground(ctx context.Context) error {
	var pending []<-chan struct{}
	e.do(func() {
		for _, t := range e.background {
			t.cancel()
			pending = append(pending, t.finished)
		}
	})
	for _, done := range pending {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
