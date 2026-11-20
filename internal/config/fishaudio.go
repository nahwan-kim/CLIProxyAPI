package config

// DefaultFishAudioBaseURL is the Fish Audio API root used when FishAudioConfig.BaseURL is empty.
const DefaultFishAudioBaseURL = "https://api.fish.audio"

// FishAudioConfig holds the upstream credential for the Fish Audio text-to-speech passthrough
// route (9ooniq). The route stays disabled (404) while APIKey is empty.
type FishAudioConfig struct {
	// APIKey authenticates requests to the Fish Audio API.
	APIKey string `yaml:"api-key,omitempty" json:"api-key,omitempty"`

	// BaseURL overrides the Fish Audio API root (default https://api.fish.audio).
	BaseURL string `yaml:"base-url,omitempty" json:"base-url,omitempty"`
}
