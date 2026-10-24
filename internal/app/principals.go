package app

import (
	"fmt"

	"messagehub/internal/apikey"
	"messagehub/internal/config"
	"messagehub/internal/domain"
	"messagehub/internal/secrets"
)

// CheckPrincipalKeys verifies that every principal's key hash variable holds
// 1–2 well-formed SHA-256 hashes (ADR-0008 D4), so a bad value stops the
// process at start or reload instead of failing requests later. Problems
// name the config field and never include the value.
func CheckPrincipalKeys(cfg *config.Config, r secrets.Resolver) error {
	var problems []domain.FieldError
	for i, p := range cfg.Principals() {
		field := fmt.Sprintf("principals[%d].api_key_sha256_ref", i)
		value, err := r.Resolve(p.APIKeySHA256Ref)
		if err == nil {
			_, err = apikey.ParseHashes(value)
		}
		if err != nil {
			problems = append(problems, domain.FieldError{Field: field, Reason: err.Error()})
		}
	}
	if len(problems) > 0 {
		return &domain.ValidationError{Problems: problems}
	}
	return nil
}
