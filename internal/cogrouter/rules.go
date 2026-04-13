package cogrouter

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadRules reads router.yaml from disk.
func LoadRules(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cogrouter: read %s: %w", path, err)
	}
	return ParseRules(data)
}

// ParseRules parses router.yaml bytes.
func ParseRules(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("cogrouter: parse yaml: %w", err)
	}
	if cfg.Default.Soul == "" {
		return nil, fmt.Errorf("cogrouter: config missing default.soul")
	}
	for i, r := range cfg.Rules {
		if r.ID == "" {
			return nil, fmt.Errorf("cogrouter: rule %d missing id", i)
		}
		if r.Assign.Soul == "" {
			return nil, fmt.Errorf("cogrouter: rule %s missing assign.soul", r.ID)
		}
	}
	return &cfg, nil
}

// matches returns true if the rule's Match criteria are satisfied by ctx.
// Empty criteria fields are wildcards. PathPrefixes matches if any touched
// path has any listed prefix.
func (r Rule) matches(ctx TaskContext) bool {
	if r.When.Type != "" && !strings.EqualFold(r.When.Type, ctx.Type) {
		return false
	}
	if r.When.Risk != "" && !strings.EqualFold(r.When.Risk, ctx.Risk) {
		return false
	}
	if r.When.Ambiguity != "" && !strings.EqualFold(r.When.Ambiguity, ctx.Ambiguity) {
		return false
	}
	if len(r.When.PathPrefixes) > 0 {
		if !anyPathMatches(ctx.TouchedPaths, r.When.PathPrefixes) {
			return false
		}
	}
	return true
}

func anyPathMatches(paths, prefixes []string) bool {
	for _, p := range paths {
		for _, pre := range prefixes {
			if strings.HasPrefix(p, pre) {
				return true
			}
		}
	}
	return false
}
