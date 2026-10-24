package config

import (
	"fmt"
	"sort"

	"messagehub/internal/domain"
)

// problems collects field errors in the order they are found.
type problems []domain.FieldError

func (p *problems) add(field, format string, args ...any) {
	*p = append(*p, domain.FieldError{Field: field, Reason: fmt.Sprintf(format, args...)})
}

// validateSemantics runs every cross-field check and returns all problems
// (§11 "语义校验"). It never stops at the first problem.
func validateSemantics(doc document) []domain.FieldError {
	var p problems
	principalIdx := checkUniqueIDs(&p, "principals", ids(doc.Principals, func(x Principal) string { return x.ID }))
	policyIdx := checkUniqueIDs(&p, "policies", ids(doc.Policies, func(x Policy) string { return x.ID }))
	destIdx := checkUniqueIDs(&p, "destinations", ids(doc.Destinations, func(x Destination) string { return x.ID }))
	checkUniqueIDs(&p, "routes", ids(doc.Routes, func(x Route) string { return x.ID }))
	checkUniqueKeyRefs(&p, doc.Principals)
	checkPolicies(&p, doc.Policies)
	for i, d := range doc.Destinations {
		if _, ok := policyIdx[d.Policy]; !ok {
			p.add(fmt.Sprintf("destinations[%d].policy", i), "unknown policy %q", d.Policy)
		}
		checkSecretUsage(&p, i, d)
	}
	for i, r := range doc.Routes {
		checkRoute(&p, i, r, doc.Principals, principalIdx, destIdx)
	}
	return p
}

func ids[T any](items []T, id func(T) string) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = id(it)
	}
	return out
}

// checkUniqueIDs reports duplicates and returns id → first index.
func checkUniqueIDs(p *problems, kind string, idList []string) map[string]int {
	first := make(map[string]int, len(idList))
	for i, id := range idList {
		if j, dup := first[id]; dup {
			p.add(fmt.Sprintf("%s[%d].id", kind, i), "duplicate id %q (first defined at %s[%d])", id, kind, j)
			continue
		}
		first[id] = i
	}
	return first
}

// checkUniqueKeyRefs rejects two principals sharing one API key reference,
// which would make authentication ambiguous.
func checkUniqueKeyRefs(p *problems, principals []Principal) {
	first := make(map[string]int, len(principals))
	for i, pr := range principals {
		if j, dup := first[pr.APIKeySHA256Ref]; dup {
			p.add(fmt.Sprintf("principals[%d].api_key_sha256_ref", i),
				"duplicate api_key_sha256_ref %q (also used by principals[%d])", pr.APIKeySHA256Ref, j)
			continue
		}
		first[pr.APIKeySHA256Ref] = i
	}
}

func checkPolicies(p *problems, policies []Policy) {
	for i, pol := range policies {
		if pol.Timeout.Std() >= pol.Retry.Backoff.Max.Std() {
			p.add(fmt.Sprintf("policies[%d].timeout", i),
				"timeout %s must be less than retry.backoff.max %s", pol.Timeout, pol.Retry.Backoff.Max)
		}
	}
}

func checkRoute(p *problems, i int, r Route, principals []Principal, principalIdx, destIdx map[string]int) {
	at := fmt.Sprintf("routes[%d]", i)
	if !domain.EventTypePattern.MatchString(r.EventType) {
		p.add(at+".event_type", "event_type %q must match %s", r.EventType, domain.EventTypePattern.String())
	}
	for j, src := range r.Sources {
		idx, ok := principalIdx[src]
		field := fmt.Sprintf("%s.sources[%d]", at, j)
		switch {
		case !ok:
			p.add(field, "unknown principal %q", src)
		case principals[idx].Role != RoleSource:
			p.add(field, "principal %q has role %q; sources must have role %q", src, principals[idx].Role, RoleSource)
		}
	}
	for j, dest := range r.Destinations {
		if _, ok := destIdx[dest]; !ok {
			p.add(fmt.Sprintf("%s.destinations[%d]", at, j), "unknown destination %q", dest)
		}
	}
}

// duplicateRouteWarnings reports event_type + destination pairs reached by
// more than one route. Loading still succeeds; deliveries are deduplicated.
func duplicateRouteWarnings(routes []Route) []string {
	type pair struct{ eventType, dest string }
	firstRoute := make(map[pair]string)
	var warnings []string
	for _, r := range routes {
		for _, d := range r.Destinations {
			key := pair{r.EventType, d}
			if prev, dup := firstRoute[key]; dup {
				warnings = append(warnings, fmt.Sprintf(
					"routes %q and %q both send event_type %q to destination %q; one delivery is created per event",
					prev, r.ID, r.EventType, d))
				continue
			}
			firstRoute[key] = r.ID
		}
	}
	sort.Strings(warnings)
	return warnings
}
