package cliproxy

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type shutdownBlockingUsagePlugin struct {
	entered  chan struct{}
	release  chan struct{}
	records  chan string
	once     sync.Once
	detached chan struct{}
}

func (p *shutdownBlockingUsagePlugin) HandleUsage(_ context.Context, r usage.Record) {
	p.once.Do(func() { close(p.entered); <-p.release })
	if !sdktranslator.HasPluginHooks() {
		select {
		case p.detached <- struct{}{}:
		default:
		}
	}
	p.records <- r.RequestID
}

// A subprocess isolates the real process-wide manager from other service shutdown tests.
func TestServiceUsageDrainBeforePluginDetach(t *testing.T) {
	mode := os.Getenv("CPA_USAGE_SHUTDOWN_FIXTURE")
	if mode == "" {
		for _, m := range []string{"drain", "cancel"} {
			t.Run(m, func(t *testing.T) {
				cmd := exec.Command(os.Args[0], "-test.run=^TestServiceUsageDrainBeforePluginDetach$", "-test.timeout=10s")
				cmd.Env = append(os.Environ(), "CPA_USAGE_SHUTDOWN_FIXTURE="+m)
				if b, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("isolated shutdown: %v\n%s", err, b)
				}
			})
		}
		return
	}
	host := pluginhost.New()
	sdktranslator.SetPluginHooks(host)
	p := &shutdownBlockingUsagePlugin{entered: make(chan struct{}), release: make(chan struct{}), records: make(chan string, 2), detached: make(chan struct{}, 2)}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(p.release) }) }
	defer release()
	usage.RegisterPlugin(p)
	usage.PublishRecord(context.Background(), usage.Record{RequestID: "first"})
	<-p.entered
	usage.PublishRecord(context.Background(), usage.Record{RequestID: "queued-final"})
	service := &Service{pluginHost: host}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if mode == "cancel" {
		cancel()
		err := service.Shutdown(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("incomplete drain reported clean: %v", err)
		}
		if !sdktranslator.HasPluginHooks() {
			t.Fatal("plugins detached before a canceled drain completed")
		}
		if err2 := service.Shutdown(context.Background()); !errors.Is(err2, context.Canceled) {
			t.Fatal("repeated shutdown erased its incomplete result")
		}
		release()
		return
	}
	done := make(chan error, 1)
	go func() { done <- service.Shutdown(ctx) }()
	select {
	case err := <-done:
		t.Fatalf("shutdown returned before blocked usage delivery: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if !sdktranslator.HasPluginHooks() {
		t.Fatal("plugin hooks detached while usage was queued")
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown failed to complete after releasing usage")
	}
	if sdktranslator.HasPluginHooks() {
		t.Fatal("plugin cleanup did not follow the drained queue")
	}
	for _, want := range []string{"first", "queued-final"} {
		select {
		case got := <-p.records:
			if got != want {
				t.Fatal("usage order changed")
			}
		default:
			t.Fatal("queued final usage missing")
		}
	}
	select {
	case <-p.detached:
		t.Fatal("usage callback ran after plugin detach")
	default:
	}
}
