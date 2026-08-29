package repository

import (
	"fmt"
	"os"
	"path/filepath"
)

func findFromWorkingDir(rel string) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		candidate := filepath.Join(dir, rel)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("%s not found from %s", rel, wd)
		}
		dir = parent
	}
}
