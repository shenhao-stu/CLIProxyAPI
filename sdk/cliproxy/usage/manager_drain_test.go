package usage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type drainPlugin struct {
	entered, release chan struct{}
	records          []string
	once             sync.Once
}

func (p *drainPlugin) HandleUsage(_ context.Context, r Record) {
	p.once.Do(func() { close(p.entered); <-p.release })
	p.records = append(p.records, r.RequestID)
}
func TestStopAndWaitDrainsAcceptedUsageBeforeCompletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewManager(1)
		p := &drainPlugin{entered: make(chan struct{}), release: make(chan struct{})}
		m.Register(p)
		m.Publish(context.Background(), Record{RequestID: "first"})
		<-p.entered
		m.Publish(context.Background(), Record{RequestID: "final"})
		done := make(chan error, 1)
		go func() { done <- m.StopAndWait(context.Background()) }()
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("drain returned before callback completed")
		default:
		}
		close(p.release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if len(p.records) != 2 || p.records[0] != "first" || p.records[1] != "final" {
			t.Fatal("accepted usage was dropped or reordered")
		}
		if err := m.StopAndWait(nil); err != nil {
			t.Fatal(err)
		}
	})
}
func TestStopAndWaitCancellationDoesNotDiscardQueue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewManager(1)
		p := &drainPlugin{entered: make(chan struct{}), release: make(chan struct{})}
		m.Register(p)
		m.Publish(context.Background(), Record{RequestID: "first"})
		<-p.entered
		m.Publish(context.Background(), Record{RequestID: "final"})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := m.StopAndWait(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("incomplete drain reported clean: %v", err)
		}
		close(p.release)
		if err := m.StopAndWait(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(p.records) != 2 {
			t.Fatal("canceled wait lost accepted usage")
		}
	})
}
func TestStopAndWaitUnusedAndConcurrentLifecycle(t *testing.T) {
	if err := (*Manager)(nil).StopAndWait(nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 32; i++ {
		m := NewManager(1)
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); m.Start(context.Background()) }()
		go func() { defer wg.Done(); m.Stop() }()
		go func() {
			defer wg.Done()
			if err := m.StopAndWait(context.Background()); err != nil {
				t.Error(err)
			}
		}()
		wg.Wait()
		if err := m.StopAndWait(nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStopAndWaitDeadlineIsNotSuccessfulFlush(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewManager(1)
		p := &drainPlugin{entered: make(chan struct{}), release: make(chan struct{})}
		m.Register(p)
		m.Publish(context.Background(), Record{RequestID: "accepted-final"})
		<-p.entered
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := m.StopAndWait(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("unflushed deadline outcome: %v", err)
		}
		close(p.release)
		if err := m.StopAndWait(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(p.records) != 1 {
			t.Fatal("deadline discarded accepted record")
		}
	})
}
