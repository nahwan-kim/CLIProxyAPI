package thinking

import (
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// strictLevels makes the proxy reject reasoning levels that the target model's
// registered thinking levels do not list, instead of clamping or passing them through.
var strictLevels atomic.Bool

// SetStrictLevels toggles strict reasoning-level enforcement (config: strict-thinking-levels).
func SetStrictLevels(enabled bool) { strictLevels.Store(enabled) }

// StrictLevelsEnabled reports whether strict reasoning-level enforcement is on.
func StrictLevelsEnabled() bool { return strictLevels.Load() }

// enforceStrictLevels returns an ErrLevelNotSupported ThinkingError (HTTP 400) when the
// request asks for an explicit reasoning level the model does not register. request is the
// client source body when available, else the translated body. Models without registered
// levels, and the none/auto selectors, are left to normal handling.
func enforceStrictLevels(request []byte, modelInfo *registry.ModelInfo, baseModel, fromFormat, providerFormat string, suffix SuffixResult, sourceConfig ThinkingConfig) error {
	if modelInfo == nil || modelInfo.Thinking == nil || len(modelInfo.Thinking.Levels) == 0 {
		return nil
	}
	var candidates []ThinkingConfig
	if suffix.HasSuffix {
		candidates = append(candidates, parseSuffixToConfig(suffix.RawSuffix, providerFormat, baseModel))
	} else {
		if hasThinkingConfig(sourceConfig) {
			candidates = append(candidates, sourceConfig)
		} else {
			candidates = append(candidates, extractThinkingConfig(request, fromFormat))
		}
		if isResponsesFormat(fromFormat) {
			candidates = append(candidates, extractConfigurationUpdateConfig(request))
		}
	}
	for _, candidate := range candidates {
		if candidate.Mode != ModeLevel {
			continue
		}
		level := strings.ToLower(strings.TrimSpace(string(candidate.Level)))
		if level == "" || level == string(LevelAuto) || level == string(LevelNone) {
			continue
		}
		if !isLevelSupported(level, modelInfo.Thinking.Levels) {
			model := modelInfo.ID
			if model == "" {
				model = baseModel
			}
			message := fmt.Sprintf("reasoning effort %q is not supported by model %s; supported: %s", level, model, strings.Join(normalizeLevels(modelInfo.Thinking.Levels), ", "))
			return NewThinkingErrorWithModel(ErrLevelNotSupported, message, model)
		}
	}
	return nil
}
