package config

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"

	"messagehub/internal/domain"
)

const exampleConfigPath = "../../config/examples/hub.example.yaml"

func mustLoad(t *testing.T, path string, opts ...Option) *Config {
	t.Helper()
	cfg, err := Load(path, opts...)
	if err != nil {
		t.Fatalf("Load(%q) error: %v", path, err)
	}
	return cfg
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	return data
}

func TestLoadExampleConfig(t *testing.T) {
	// Act
	cfg := mustLoad(t, exampleConfigPath)

	// Assert
	if cfg.Version() != 1 {
		t.Errorf("Version() = %d, want 1", cfg.Version())
	}
	if len(cfg.Warnings()) != 0 {
		t.Errorf("Warnings() = %v, want none", cfg.Warnings())
	}
	if got := len(cfg.Hash()); got != 64 {
		t.Errorf("Hash() length = %d, want 64 hex chars", got)
	}
	wantCounts := map[string][2]int{
		"principals":   {len(cfg.Principals()), 5},
		"policies":     {len(cfg.Policies()), 3},
		"destinations": {len(cfg.Destinations()), 4},
		"routes":       {len(cfg.Routes()), 3},
	}
	for name, c := range wantCounts {
		if c[0] != c[1] {
			t.Errorf("len(%s) = %d, want %d", name, c[0], c[1])
		}
	}
}

func TestExampleLookupTables(t *testing.T) {
	cfg := mustLoad(t, exampleConfigPath)

	tests := []struct {
		name      string
		eventType string
		wantDests []string
	}{
		{"registration goes to ads", "user.registered", []string{"ad-platform"}},
		{"payment goes to crm", "payment.succeeded", []string{"crm"}},
		{"purchase fans out", "order.purchased", []string{"inventory", "data-warehouse"}},
		{"unknown type has no routes", "order.cancelled", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			routes := cfg.RoutesForEventType(tt.eventType)

			// Assert
			var got []string
			for _, r := range routes {
				got = append(got, r.Destinations...)
			}
			if !reflect.DeepEqual(got, tt.wantDests) {
				t.Fatalf("destinations = %v, want %v", got, tt.wantDests)
			}
		})
	}
}

func TestExampleScenarioDetails(t *testing.T) {
	cfg := mustLoad(t, exampleConfigPath)

	crm, ok := cfg.Destination("crm")
	if !ok || crm.Auth.Type != AuthOAuth2ClientCredentials || crm.Request.Method != "PATCH" {
		t.Errorf("crm = %+v, want oauth2 PATCH destination", crm)
	}
	if crm.MaxInFlight != DefaultMaxInFlight {
		t.Errorf("crm.MaxInFlight = %d, want default %d", crm.MaxInFlight, DefaultMaxInFlight)
	}
	inv, _ := cfg.Destination("inventory")
	if inv.Auth.Type != AuthNone {
		t.Errorf("inventory auth = %q, want default %q", inv.Auth.Type, AuthNone)
	}
	pol, ok := cfg.Policy(inv.Policy)
	if !ok || pol.Ordering.Mode != OrderingPerKey || pol.Ordering.OnPredecessorDead != domain.PredecessorDeadBlock {
		t.Errorf("inventory policy ordering = %+v, want per_key/block", pol.Ordering)
	}
	ads, _ := cfg.Policy("ad-platform")
	if len(ads.Success.Body) != 1 || ads.Success.Body[0].Op != OpEq || !ads.Success.Status[0].Matches(200) {
		t.Errorf("ad-platform success = %+v, want 2xx + code eq 0", ads.Success)
	}
	if p, ok := cfg.Principal("oncall-operator"); !ok || p.Role != RoleOperator {
		t.Errorf("oncall-operator = %+v, want operator", p)
	}
}

func TestExampleHasNoPlaintextCredentials(t *testing.T) {
	cfg := mustLoad(t, exampleConfigPath)
	for _, d := range cfg.Destinations() {
		for name, ref := range d.Secrets {
			if !strings.HasPrefix(ref, "env:") {
				t.Errorf("destination %s secret %s = %q, want env: reference", d.ID, name, ref)
			}
		}
	}
}

func TestLookupMisses(t *testing.T) {
	cfg := mustLoad(t, exampleConfigPath)
	if _, ok := cfg.Destination("nope"); ok {
		t.Error("Destination(nope) found")
	}
	if _, ok := cfg.Policy("nope"); ok {
		t.Error("Policy(nope) found")
	}
	if _, ok := cfg.Principal("nope"); ok {
		t.Error("Principal(nope) found")
	}
}

func TestAccessorsReturnCopies(t *testing.T) {
	// Arrange
	cfg := mustLoad(t, exampleConfigPath)
	hashBefore := cfg.Hash()

	// Act: mutate everything a caller can reach.
	d, _ := cfg.Destination("inventory")
	d.Request.Headers["Authorization"] = "tampered"
	d.Secrets["INVENTORY_TOKEN"] = "tampered"
	d.Request.Body.(map[string]any)["reason"] = "tampered"
	p, _ := cfg.Policy("ad-platform")
	p.Success.Body[0].Path = "tampered"
	p.Success.Status[0] = StatusMatcher{Code: 999}
	r := cfg.RoutesForEventType("order.purchased")
	r[0].Destinations[0] = "tampered"
	cfg.Routes()[0].Sources[0] = "tampered"
	cfg.Warnings()

	// Assert
	d2, _ := cfg.Destination("inventory")
	p2, _ := cfg.Policy("ad-platform")
	r2 := cfg.RoutesForEventType("order.purchased")
	switch {
	case d2.Request.Headers["Authorization"] == "tampered", d2.Secrets["INVENTORY_TOKEN"] == "tampered",
		d2.Request.Body.(map[string]any)["reason"] == "tampered":
		t.Error("destination mutation leaked into config")
	case p2.Success.Body[0].Path == "tampered", p2.Success.Status[0].Code == 999:
		t.Error("policy mutation leaked into config")
	case r2[0].Destinations[0] == "tampered", cfg.Routes()[0].Sources[0] == "tampered":
		t.Error("route mutation leaked into config")
	}
	if cfg.Hash() != hashBefore {
		t.Error("hash changed after caller mutation")
	}
}

func TestConfigHash(t *testing.T) {
	base := mustRead(t, "testdata/valid/minimal.yaml")
	tests := []struct {
		name     string
		mutate   func([]byte) []byte
		wantSame bool
	}{
		{"identical bytes", func(b []byte) []byte { return b }, true},
		{"comment only", func(b []byte) []byte { return append([]byte("# note\n"), b...) }, true},
		{"explicit default max_in_flight", func(b []byte) []byte {
			return bytes.Replace(b, []byte("    policy: default\n"), []byte("    policy: default\n    max_in_flight: 10\n"), 1)
		}, true},
		{"changed timeout", func(b []byte) []byte {
			return bytes.Replace(b, []byte("timeout: 5s"), []byte("timeout: 6s"), 1)
		}, false},
		{"changed body", func(b []byte) []byte {
			return bytes.Replace(b, []byte("status: paid"), []byte("status: unpaid"), 1)
		}, false},
	}
	first, err := Parse(base)
	if err != nil {
		t.Fatalf("Parse(base): %v", err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			second, err := Parse(tt.mutate(bytes.Clone(base)))

			// Assert
			if err != nil {
				t.Fatalf("Parse(mutated): %v", err)
			}
			if same := first.Hash() == second.Hash(); same != tt.wantSame {
				t.Fatalf("hash equal = %v, want %v", same, tt.wantSame)
			}
		})
	}
}

func TestExampleHashStableAcrossLoads(t *testing.T) {
	a := mustLoad(t, exampleConfigPath)
	b := mustLoad(t, exampleConfigPath)
	if a.Hash() != b.Hash() {
		t.Fatalf("hash differs across loads: %s vs %s", a.Hash(), b.Hash())
	}
}

func TestDuplicateRouteWarning(t *testing.T) {
	// Act
	cfg := mustLoad(t, "testdata/valid/duplicate_route.yaml")

	// Assert
	w := cfg.Warnings()
	if len(w) != 1 {
		t.Fatalf("Warnings() = %v, want exactly 1", w)
	}
	for _, want := range []string{"order-paid", "order-paid-again", "crm"} {
		if !strings.Contains(w[0], want) {
			t.Errorf("warning %q missing %q", w[0], want)
		}
	}
	if got := len(cfg.RoutesForEventType("order.paid")); got != 2 {
		t.Errorf("RoutesForEventType = %d routes, want 2", got)
	}
}

func TestAccessorsReturnCopiesOfArraysInBody(t *testing.T) {
	// Arrange: a body template holding an array of objects and scalars.
	minimal := string(mustRead(t, "testdata/valid/minimal.yaml"))
	withArray := strings.Replace(minimal, "        status: paid\n",
		"        status: paid\n        items: [{sku: A1}, 2]\n", 1)
	cfg, err := Parse([]byte(withArray))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Act: mutate the array and the object inside it on the returned copy.
	d, _ := cfg.Destination("crm")
	items := d.Request.Body.(map[string]any)["items"].([]any)
	items[0].(map[string]any)["sku"] = "tampered"
	items[1] = "tampered"

	// Assert
	d2, _ := cfg.Destination("crm")
	items2 := d2.Request.Body.(map[string]any)["items"].([]any)
	if items2[0].(map[string]any)["sku"] != "A1" || items2[1] == "tampered" {
		t.Fatalf("array mutation leaked into config: %v", items2)
	}
}

func TestOmittedOrderingDefaultsToModeNone(t *testing.T) {
	// Arrange
	minimal := string(mustRead(t, "testdata/valid/minimal.yaml"))
	noOrdering := strings.Replace(minimal, "    ordering: { mode: none }\n", "", 1)
	if noOrdering == minimal {
		t.Fatal("fixture no longer contains the ordering line this test removes")
	}

	// Act
	cfg, err := Parse([]byte(noOrdering))

	// Assert
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p, _ := cfg.Policy("default")
	if p.Ordering.Mode != OrderingNone || p.Ordering.OnPredecessorDead != "" {
		t.Fatalf("Ordering = %+v, want mode none without on_predecessor_dead", p.Ordering)
	}
}

func TestPerKeyDefaultsToBlock(t *testing.T) {
	cfg := mustLoad(t, "testdata/valid/per_key_default.yaml")
	p, _ := cfg.Policy("default")
	if p.Ordering.OnPredecessorDead != domain.PredecessorDeadBlock {
		t.Fatalf("OnPredecessorDead = %q, want %q", p.Ordering.OnPredecessorDead, domain.PredecessorDeadBlock)
	}
}

func TestModeNoneHasNoPredecessorRule(t *testing.T) {
	cfg := mustLoad(t, "testdata/valid/minimal.yaml")
	p, _ := cfg.Policy("default")
	if p.Ordering.Mode != OrderingNone || p.Ordering.OnPredecessorDead != "" {
		t.Fatalf("Ordering = %+v, want mode none without on_predecessor_dead", p.Ordering)
	}
}
