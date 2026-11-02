package watcher

import (
	"os"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestConfigReloadKeepsStateWhenSecondReadSeesTruncation(t *testing.T) {
	path, authDir := newIssue6631ConfigFile(t)
	if err := writeIssue6631Config(path, authDir, 1); err != nil {
		t.Fatal(err)
	}
	var applied []int
	w := &Watcher{configPath: path, authDir: authDir, reloadCallback: func(cfg *config.Config) { applied = append(applied, cfg.RequestRetry) }}
	w.ReloadConfigIfChanged()
	if err := writeIssue6631Config(path, authDir, 2); err != nil {
		t.Fatal(err)
	}
	originalLoad := loadConfigFileFunc
	t.Cleanup(func() { loadConfigFileFunc = originalLoad })
	loadConfigFileFunc = func(file string) (*config.Config, error) {
		// Deterministically model a writer's truncate between the hash read and the loader read.
		if err := os.WriteFile(file, nil, 0600); err != nil {
			return nil, err
		}
		return originalLoad(file)
	}
	w.ReloadConfigIfChanged()
	if !reflect.DeepEqual(applied, []int{1}) {
		t.Fatalf("empty snapshot replaced runtime state: %v", applied)
	}
	if w.config.AuthDir != authDir || w.config.RequestRetry != 1 {
		t.Fatal("last valid config was lost")
	}
	loadConfigFileFunc = originalLoad
	if err := writeIssue6631Config(path, authDir, 2); err != nil {
		t.Fatal(err)
	}
	w.ReloadConfigIfChanged()
	if !reflect.DeepEqual(applied, []int{1, 2}) {
		t.Fatalf("valid completed save was not applied: %v", applied)
	}
}
