// Package routing decides which routes and destinations an accepted event
// goes to (docs/design/domain-model.md §4). It is pure: it reads the config
// snapshot it is given and never touches I/O. It depends on domain, config
// and rules.
package routing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"messagehub/internal/config"
	"messagehub/internal/domain"
	"messagehub/internal/rules"
)

// Match returns, in config order, the ids of the routes the event matches
// and the union of their destinations, de-duplicated in first-seen order.
// Both slices are empty (never nil) when nothing matches.
func Match(cfg *config.Config, env domain.Envelope, source string) (routeIDs, destinationIDs []string, err error) {
	routeIDs, destinationIDs = []string{}, []string{}
	candidates := cfg.RoutesForEventType(env.EventType)
	if len(candidates) == 0 {
		return routeIDs, destinationIDs, nil
	}
	doc, err := matchDocument(env)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range candidates {
		if len(r.Sources) > 0 && !slices.Contains(r.Sources, source) {
			continue
		}
		if !rules.EvalAll(r.Match, doc) {
			continue
		}
		routeIDs = append(routeIDs, r.ID)
		for _, d := range r.Destinations {
			if !slices.Contains(destinationIDs, d) {
				destinationIDs = append(destinationIDs, d)
			}
		}
	}
	return routeIDs, destinationIDs, nil
}

// matchDocument is the root that match rule paths start from: payload.* and
// metadata.*. Numbers stay json.Number so rules compare them exactly.
func matchDocument(env domain.Envelope) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(env.Payload))
	dec.UseNumber()
	var payload any
	if err := dec.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode payload for routing: %w", err)
	}
	metadata := make(map[string]any, len(env.Metadata))
	for k, v := range env.Metadata {
		metadata[k] = v
	}
	return map[string]any{"payload": payload, "metadata": metadata}, nil
}
