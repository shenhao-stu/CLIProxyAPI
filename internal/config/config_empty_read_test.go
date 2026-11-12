package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStrictConfigLoadRejectsTransientEmptyFile(t *testing.T) {
	for _, raw := range []string{"", " \t\n"} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if cfg, err := LoadConfig(path); err == nil || cfg != nil {
			t.Fatal("strict load accepted an empty file as a replacement runtime configuration")
		}
		if cfg, err := LoadConfigOptional(path, true); err != nil || cfg == nil {
			t.Fatal("optional cloud standby behavior changed")
		}
	}
}
