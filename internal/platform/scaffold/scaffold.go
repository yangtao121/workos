// Package scaffold creates intentionally empty module boundaries for future
// WorkOS contributors without inventing business behavior.
package scaffold

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

var processRoots = map[string]string{
	"workos-gateway":   "gateway",
	"workos-core":      "core",
	"harness-host":     "harness",
	"runtime-host":     "runtime",
	"reliability-host": "reliability",
	"indexer":          "indexer",
}

// CreateModule adds the standard domain/application/ports/adapters/transport
// folders beneath one stable process. It refuses to overwrite any path.
func CreateModule(repositoryRoot, process, name string) (string, error) {
	processRoot, ok := processRoots[process]
	if !ok {
		return "", fmt.Errorf("unknown process %q", process)
	}
	if !namePattern.MatchString(name) {
		return "", errors.New("module name must match ^[a-z][a-z0-9]*$")
	}
	target := filepath.Join(repositoryRoot, "internal", processRoot, name)
	if _, err := os.Stat(target); err == nil {
		return "", fmt.Errorf("module already exists: %s", target)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect module target: %w", err)
	}

	layers := []string{"domain", "application", "ports", "adapters", "transport"}
	for _, layer := range layers {
		directory := filepath.Join(target, layer)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return "", fmt.Errorf("create %s: %w", layer, err)
		}
		document := fmt.Sprintf("// Package %s is the %s layer of the %s module.\npackage %s\n", layer, layer, name, layer)
		if err := writeExclusive(filepath.Join(directory, "doc.go"), document); err != nil {
			return "", err
		}
	}
	readme := fmt.Sprintf("# %s\n\nProcess owner: `%s`\n\nStatus: scaffolded. Define behavior and acceptance tests before marking it working.\n", name, process)
	if err := writeExclusive(filepath.Join(target, "README.md"), readme); err != nil {
		return "", err
	}
	return target, nil
}

func writeExclusive(path, content string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := strings.NewReader(content).WriteTo(file); err != nil {
		file.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}
