package app

import (
	"errors"
	"strings"
	"testing"

	"messagehub/internal/config"
	"messagehub/internal/domain"
	"messagehub/internal/secrets"
)

func TestCheckPrincipalKeys(t *testing.T) {
	cfg, err := config.Load(exampleConfig)
	if err != nil {
		t.Fatalf("load example: %v", err)
	}
	principals := cfg.Principals()
	badName := strings.TrimPrefix(principals[1].APIKeySHA256Ref, config.EnvRefPrefix)
	tests := []struct {
		name      string
		override  map[string]string
		wantField string
	}{
		{"valid hashes pass", nil, ""},
		{"two hashes during rotation pass", map[string]string{badName: fakeHash + "," + strings.Repeat("b", 64)}, ""},
		{"malformed hash is rejected", map[string]string{badName: "not-a-hash"}, "principals[1].api_key_sha256_ref"},
		{"missing variable is rejected", map[string]string{badName: ""}, "principals[1].api_key_sha256_ref"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			env := fakeEnv(cfg)
			for k, v := range tt.override {
				env[k] = v
			}

			// Act
			err := CheckPrincipalKeys(cfg, secrets.NewEnvResolver(lookupFrom(env)))

			// Assert
			if tt.wantField == "" {
				if err != nil {
					t.Fatalf("CheckPrincipalKeys error: %v", err)
				}
				return
			}
			var verr *domain.ValidationError
			if !errors.As(err, &verr) || !verr.HasField(tt.wantField) {
				t.Fatalf("error = %v, want a problem on %s", err, tt.wantField)
			}
			if strings.Contains(err.Error(), "not-a-hash") {
				t.Errorf("error %q echoes the variable value", err)
			}
		})
	}
}
