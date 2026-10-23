package config

import "maps"

// Deep copies used by accessors so callers never share mutable state with
// the loaded *Config.

func (p Policy) clone() Policy {
	out := p
	out.Retry.RetryOn.Status = cloneSlice(p.Retry.RetryOn.Status)
	out.Success.Status = cloneSlice(p.Success.Status)
	out.Success.Body = cloneRules(p.Success.Body)
	return out
}

func (d Destination) clone() Destination {
	out := d
	out.Secrets = maps.Clone(d.Secrets)
	out.Auth.Scopes = cloneSlice(d.Auth.Scopes)
	out.Request.Headers = maps.Clone(d.Request.Headers)
	out.Request.Body = cloneJSONValue(d.Request.Body)
	return out
}

func (r Route) clone() Route {
	out := r
	out.Sources = cloneSlice(r.Sources)
	out.Match = cloneRules(r.Match)
	out.Destinations = cloneSlice(r.Destinations)
	return out
}

func cloneRules(rules []Rule) []Rule {
	if rules == nil {
		return nil
	}
	out := make([]Rule, len(rules))
	for i, r := range rules {
		out[i] = Rule{Path: r.Path, Op: r.Op, Value: cloneJSONValue(r.Value)}
	}
	return out
}

// cloneSlice copies a slice of values, keeping nil as nil.
func cloneSlice[T any](in []T) []T {
	if in == nil {
		return nil
	}
	return append(make([]T, 0, len(in)), in...)
}

// cloneJSONValue deep-copies a decoded JSON value (maps, slices, scalars).
func cloneJSONValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, child := range t {
			out[k] = cloneJSONValue(child)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, child := range t {
			out[i] = cloneJSONValue(child)
		}
		return out
	default:
		return v
	}
}

func cloneAll[T any](in []T, cloneFn func(T) T) []T {
	out := make([]T, len(in))
	for i, v := range in {
		out[i] = cloneFn(v)
	}
	return out
}
