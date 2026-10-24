package config

import (
	"sort"
	"strings"
)

// EnvRefPrefix is the only secret reference scheme supported today (ADR-0008 D3).
// Other secret managers will be added as new schemes behind the same lookup.
const EnvRefPrefix = "env:"

// RequiredEnv returns the sorted, de-duplicated names of every environment
// variable the configuration references: principal key hashes, OAuth2 client
// credentials and destination secrets. It never reads the variables. The list
// is what a deployment must provide, e.g. the keys of a Kubernetes Secret.
func (c *Config) RequiredEnv() []string {
	seen := make(map[string]bool)
	add := func(ref string) {
		if name, ok := strings.CutPrefix(ref, EnvRefPrefix); ok && name != "" {
			seen[name] = true
		}
	}
	for _, p := range c.doc.Principals {
		add(p.APIKeySHA256Ref)
	}
	for _, d := range c.doc.Destinations {
		add(d.Auth.ClientIDRef)
		add(d.Auth.ClientSecretRef)
		for _, ref := range d.Secrets {
			add(ref)
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
