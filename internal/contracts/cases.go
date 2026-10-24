// Package contracts loads the golden cases under testdata/contracts.
//
// The cases are the acceptance contract for the template engine, the
// success evaluator and the retry delay calculator (docs/design/domain-model.md
// §7.1–§7.4). This package only decodes and validates the case files; it does
// not implement any of that behaviour.
package contracts

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"go.yaml.in/yaml/v3"
)

// Dir returns the absolute path of the repository's testdata/contracts
// directory. It is derived from this source file's location, so it works from
// tests in any package (but not from binaries built with -trimpath).
func Dir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata", "contracts")
}

// TemplateCasesPath is the path of the template golden cases.
func TemplateCasesPath() string { return filepath.Join(Dir(), "template", "cases.yaml") }

// SuccessCasesPath is the path of the success-evaluation golden cases.
func SuccessCasesPath() string { return filepath.Join(Dir(), "success", "cases.yaml") }

// RetryCasesPath is the path of the retry-delay golden cases.
func RetryCasesPath() string { return filepath.Join(Dir(), "retry", "cases.yaml") }

// rawCase is implemented by the YAML-level representation of each case kind.
type rawCase interface {
	caseName() string
}

type caseFile[R rawCase] struct {
	Cases []R `yaml:"cases"`
}

// loadCases strictly decodes a case file, converts every case and returns all
// problems at once (unknown fields, duplicate names, invalid values).
func loadCases[R rawCase, C any](path string, convert func(R) (C, error)) ([]C, error) {
	raws, err := decodeFile[R](path)
	if err != nil {
		return nil, err
	}
	if len(raws) == 0 {
		return nil, fmt.Errorf("load %q: no cases", path)
	}

	seen := make(map[string]bool, len(raws))
	cases := make([]C, 0, len(raws))
	var problems []error
	for i, raw := range raws {
		name := raw.caseName()
		if seen[name] {
			problems = append(problems, fmt.Errorf("cases[%d]: duplicate name %q", i, name))
		}
		seen[name] = true

		c, err := convert(raw)
		if err != nil {
			problems = append(problems, fmt.Errorf("cases[%d] %q: %w", i, name, err))
			continue
		}
		cases = append(cases, c)
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("load %q: %w", path, errors.Join(problems...))
	}
	return cases, nil
}

func decodeFile[R rawCase](path string) ([]R, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("load %q: %w", path, err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	var file caseFile[R]
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("load %q: %w", path, err)
	}
	return file.Cases, nil
}

// isSet reports whether a yaml.Node field was present in the document
// (an explicit null counts as present).
func isSet(n yaml.Node) bool { return n.Kind != 0 }

// jsonValue converts a YAML value to the value encoding/json would produce
// for the same JSON document (objects → map[string]any, numbers → float64).
func jsonValue(n yaml.Node) (any, error) {
	var v any
	if err := n.Decode(&v); err != nil {
		return nil, err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("value is not representable as JSON: %w", err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func requireName(name string) error {
	if name == "" {
		return errors.New("name is required")
	}
	return nil
}
