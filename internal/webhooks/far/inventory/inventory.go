// Package inventory loads the list of fence agent names that the WAO init
// container collects from the selected FAR image. FAR admission accepts an
// agent only if its exact name is in the list.
package inventory

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/medik8s/fence-agents-remediation/v5/pkg/validation"
)

const (
	maxNames     = 1024
	maxNameBytes = 255
	// Each line is a name and a newline character.
	maxFileBytes = maxNames * (maxNameBytes + 1)
)

var namePattern = regexp.MustCompile(`^fence_[^/\s]+$`)

// Load reads the inventory at path and returns a checker for exact agent names.
// It returns an error unless the file is a complete and valid name list.
// The checker is a snapshot. It never reads the file again and never returns an error.
func Load(path string) (validation.AgentExists, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open FAR agent inventory: %w", err)
	}
	defer func() { _ = file.Close() }()

	// Read one byte more than the limit to detect a file that is too large.
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read FAR agent inventory %s: %w", path, err)
	}
	agents, err := parse(data)
	if err != nil {
		return nil, fmt.Errorf("invalid FAR agent inventory %s: %w", path, err)
	}
	return func(agent string) (bool, error) {
		_, listed := agents[agent]
		return listed, nil
	}, nil
}

func parse(data []byte) (map[string]struct{}, error) {
	switch {
	case len(data) == 0:
		return nil, errors.New("the file is empty")
	case len(data) > maxFileBytes:
		return nil, fmt.Errorf("the file is larger than %d bytes", maxFileBytes)
	case !utf8.Valid(data):
		return nil, errors.New("the file is not UTF-8 text")
	case data[len(data)-1] != '\n':
		return nil, errors.New("the last line has no newline character")
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) > maxNames {
		return nil, fmt.Errorf("the file contains %d names, the limit is %d", len(lines), maxNames)
	}
	agents := make(map[string]struct{}, len(lines))
	for i, name := range lines {
		switch {
		case name == "":
			return nil, fmt.Errorf("line %d is empty", i+1)
		case len(name) > maxNameBytes:
			return nil, fmt.Errorf("line %d: the name is longer than %d bytes", i+1, maxNameBytes)
		case !namePattern.MatchString(name):
			return nil, fmt.Errorf("line %d: %q is not a fence agent name", i+1, name)
		}
		if _, duplicate := agents[name]; duplicate {
			return nil, fmt.Errorf("line %d: duplicate name %q", i+1, name)
		}
		agents[name] = struct{}{}
	}
	return agents, nil
}
