package config

import (
	"slices"
	"testing"
)

func TestParseConfigBytesNormalizesProviderAllowedAPIKeys(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("provider-allowed-api-keys:\n  Claude:\n    - \" owner-key \"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ProviderAllowedAPIKeys["claude"]; !slices.Equal(got, []string{"owner-key"}) {
		t.Fatalf("provider-allowed-api-keys = %v, want claude: [owner-key]", cfg.ProviderAllowedAPIKeys)
	}
}

func TestV8MigrationKeeps9ooniqSettings(t *testing.T) {
	raw := []byte(`proxy-url: old
strict-thinking-levels: true
typesafe:
  api-key: typesafe-key
  base-url: https://typesafe.example
provider-allowed-api-keys:
  claude:
    - owner-key
`)
	migrated, changed, err := NormalizeConfigLayout(raw, true)
	if err != nil || !changed {
		t.Fatalf("migrate legacy file: changed=%v error=%v", changed, err)
	}
	if err = ValidateV8Config(migrated); err != nil {
		t.Fatalf("migrated file is invalid: %v\n%s", err, migrated)
	}
	cfg, err := ParseConfigBytes(migrated)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ProviderAllowedAPIKeys["claude"]; !slices.Equal(got, []string{"owner-key"}) {
		t.Errorf("migration dropped provider-allowed-api-keys: %v", cfg.ProviderAllowedAPIKeys)
	}
	if !cfg.StrictThinkingLevels {
		t.Error("migration dropped strict-thinking-levels")
	}
	if cfg.TypeSafe.APIKey != "typesafe-key" || cfg.TypeSafe.BaseURL != "https://typesafe.example" {
		t.Errorf("migration dropped typesafe: %+v", cfg.TypeSafe)
	}
	if t.Failed() {
		t.Logf("migrated file:\n%s", migrated)
	}
}
