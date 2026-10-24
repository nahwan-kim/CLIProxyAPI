package config

// DefaultTypeSafeBaseURL is the TypeSafe API root used when TypeSafeConfig.BaseURL is empty.
const DefaultTypeSafeBaseURL = "https://api.typesafe.ai"

// TypeSafeConfig holds the upstream credential for the System One passthrough route (9ooniq).
// The route stays disabled (404) while APIKey is empty.
type TypeSafeConfig struct {
	// APIKey authenticates requests to the TypeSafe API.
	APIKey string `yaml:"api-key,omitempty" json:"api-key,omitempty"`

	// BaseURL overrides the TypeSafe API root (default https://api.typesafe.ai).
	BaseURL string `yaml:"base-url,omitempty" json:"base-url,omitempty"`
}
