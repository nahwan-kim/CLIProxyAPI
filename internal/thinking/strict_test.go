package thinking

import (
	"errors"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func strictTestModel() *registry.ModelInfo {
	return &registry.ModelInfo{ID: "grok-test", Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh"}}}
}

func TestEnforceStrictLevelsRejectsUnregisteredLevel(t *testing.T) {
	body := []byte(`{"model":"grok-test","reasoning":{"effort":"max"}}`)
	cfg := extractCodexUsageConfig(body)
	err := enforceStrictLevels(body, nil, strictTestModel(), "grok-test", "openai-response", "xai", SuffixResult{}, cfg)
	var te *ThinkingError
	if !errors.As(err, &te) || te.Code != ErrLevelNotSupported {
		t.Fatalf("expected ErrLevelNotSupported, got %v", err)
	}
	if te.StatusCode() != 400 {
		t.Fatalf("expected HTTP 400, got %d", te.StatusCode())
	}
}

func TestEnforceStrictLevelsAllowsRegisteredAndSpecialLevels(t *testing.T) {
	for _, effort := range []string{"xhigh", "low", "none", "auto"} {
		body := []byte(`{"model":"grok-test","reasoning":{"effort":"` + effort + `"}}`)
		if err := enforceStrictLevels(body, nil, strictTestModel(), "grok-test", "openai-response", "xai", SuffixResult{}, extractCodexUsageConfig(body)); err != nil {
			t.Fatalf("effort %s: unexpected error %v", effort, err)
		}
	}
}

func TestEnforceStrictLevelsChatCompletionsAndUnknownModel(t *testing.T) {
	body := []byte(`{"model":"grok-test","reasoning_effort":"minimal"}`)
	if err := enforceStrictLevels(body, nil, strictTestModel(), "grok-test", "openai", "xai", SuffixResult{}, ThinkingConfig{}); err == nil {
		t.Fatal("expected chat-completions minimal to be rejected")
	}
	if err := enforceStrictLevels(body, nil, &registry.ModelInfo{ID: "no-levels"}, "no-levels", "openai", "openai", SuffixResult{}, ThinkingConfig{}); err != nil {
		t.Fatalf("model without registered levels must pass: %v", err)
	}
}

func TestApplyThinkingStrictFlag(t *testing.T) {
	body := []byte(`{"model":"grok-test","reasoning":{"effort":"max"}}`)
	defer SetStrictLevels(false)
	SetStrictLevels(false)
	if _, err := ApplyThinkingWithModelInfo(body, body, "grok-test", "openai-response", "xai", "xai", strictTestModel()); err != nil {
		t.Fatalf("flag off must keep legacy clamping, got %v", err)
	}
	SetStrictLevels(true)
	if _, err := ApplyThinkingWithModelInfo(body, body, "grok-test", "openai-response", "xai", "xai", strictTestModel()); err == nil {
		t.Fatal("flag on must reject max for a low..xhigh model")
	}
}
