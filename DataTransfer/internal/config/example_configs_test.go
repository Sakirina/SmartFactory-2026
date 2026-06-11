package config

import (
	"path/filepath"
	"testing"
)

// TestExampleConfigsAreLoadable 保证仓库内示例配置始终可被加载与校验通过。
func TestExampleConfigsAreLoadable(t *testing.T) {
	examples, err := filepath.Glob(filepath.Join("..", "..", "configs", "*.yaml"))
	if err != nil {
		t.Fatalf("glob configs: %v", err)
	}
	if len(examples) == 0 {
		t.Fatal("no example configs found under configs/")
	}
	for _, path := range examples {
		if _, err := Load(path); err != nil {
			t.Fatalf("example config %s failed to load: %v", path, err)
		}
	}
}
