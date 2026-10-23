package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"messagehub/internal/domain"
)

// Credential rules for destinations (docs/design/domain-model.md §11 "凭据字段"):
// every secret.NAME used in a template must be declared, and fields whose names
// carry credentials must take their value from a secret, never a literal.
var (
	templateExprPattern = regexp.MustCompile(`\{\{(.*?)\}\}`)
	secretRefPattern    = regexp.MustCompile(`\bsecret\.([A-Z][A-Z0-9_]*)`)
)

const (
	allowHeaderPrefix = "headers."
	allowQueryPrefix  = "url.query."
	allowBodyPrefix   = "body."
	reasonLiteralCred = "%s %q carries a credential: use a declared secret ({{secret.NAME}}) instead of a literal value, or list %q in allow_literal"
)

// secretRefs returns the secret names referenced inside the template
// expressions of s, in order of appearance.
func secretRefs(s string) []string {
	var names []string
	for _, expr := range templateExprPattern.FindAllStringSubmatch(s, -1) {
		for _, m := range secretRefPattern.FindAllStringSubmatch(expr[1], -1) {
			names = append(names, m[1])
		}
	}
	return names
}

// secretCheck holds the state for checking one destination.
type secretCheck struct {
	p       *problems
	d       Destination
	allowed map[string]bool // normalized allow_literal entry → used
}

func checkSecretUsage(p *problems, i int, d Destination) {
	c := secretCheck{p: p, d: d, allowed: make(map[string]bool, len(d.AllowLiteral))}
	for _, entry := range d.AllowLiteral {
		c.allowed[normalizeAllowEntry(entry)] = false
	}
	at := fmt.Sprintf("destinations[%d]", i)
	c.checkURL(at + ".request.url")
	for _, name := range sortedKeys(d.Request.Headers) {
		field := joinField(at+".request.headers", name)
		c.checkValue(field, "header", name, allowHeaderPrefix+name, d.Request.Headers[name])
	}
	c.walkBody(at+".request.body", strings.TrimSuffix(allowBodyPrefix, "."), "", d.Request.Body)
	for j, entry := range d.AllowLiteral {
		if !c.allowed[normalizeAllowEntry(entry)] {
			p.add(fmt.Sprintf("%s.allow_literal[%d]", at, j),
				"entry %q does not match any credential field holding a literal value; remove it", entry)
		}
	}
}

// checkValue verifies declared secrets and, for credential-named fields,
// that the value comes from a secret unless explicitly allowed.
func (c secretCheck) checkValue(field, kind, name, allowKey, value string) {
	refs := c.checkDeclared(field, value)
	if !domain.IsSensitiveName(name) || len(refs) > 0 || c.allow(allowKey) {
		return
	}
	c.p.add(field, reasonLiteralCred, kind, name, allowKey)
}

func (c secretCheck) checkDeclared(field, value string) []string {
	refs := secretRefs(value)
	for _, name := range refs {
		if _, ok := c.d.Secrets[name]; !ok {
			c.p.add(field, "references undeclared secret %q; declare it under secrets", name)
		}
	}
	return refs
}

// allow reports whether key is in allow_literal and marks the entry as used.
func (c secretCheck) allow(key string) bool {
	norm := normalizeAllowEntry(key)
	if _, ok := c.allowed[norm]; !ok {
		return false
	}
	c.allowed[norm] = true
	return true
}

func (c secretCheck) checkURL(field string) {
	url := c.d.Request.URL
	c.checkDeclared(field, url)
	q := strings.IndexByte(url, '?')
	if q < 0 {
		return
	}
	for _, pair := range strings.Split(url[q+1:], "&") {
		name, value, _ := strings.Cut(pair, "=")
		if name == "" || !domain.IsSensitiveName(name) || len(secretRefs(value)) > 0 {
			continue
		}
		if !c.allow(allowQueryPrefix + name) {
			c.p.add(field, reasonLiteralCred, "query parameter", name, allowQueryPrefix+name)
		}
	}
}

// walkBody checks every leaf; key is the object key the leaf sits under.
func (c secretCheck) walkBody(field, allowPath, key string, v any) {
	switch t := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(t) {
			c.walkBody(joinField(field, k), allowPath+"."+k, k, t[k])
		}
	case []any:
		for i, child := range t {
			idx := strconv.Itoa(i)
			c.walkBody(joinIndex(field, idx), allowPath+"."+idx, key, child)
		}
	case string:
		c.checkValue(field, "body field", key, allowPath, t)
	case nil:
		// null holds no credential
	default: // number or bool: always a literal
		if domain.IsSensitiveName(key) && !c.allow(allowPath) {
			c.p.add(field, reasonLiteralCred, "body field", key, allowPath)
		}
	}
}

// normalizeAllowEntry makes header names case-insensitive; query and body
// names stay exact because vendors treat them as case-sensitive.
func normalizeAllowEntry(entry string) string {
	if strings.HasPrefix(strings.ToLower(entry), allowHeaderPrefix) {
		return allowHeaderPrefix + strings.ToLower(entry[len(allowHeaderPrefix):])
	}
	return entry
}
