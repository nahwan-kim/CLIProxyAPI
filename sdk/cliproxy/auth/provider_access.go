package auth

import (
	"context"
	"net/http"
	"slices"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// ErrorCodeProviderAccessDenied marks a request rejected because the client API key may not use
// any provider that serves the requested model.
const ErrorCodeProviderAccessDenied = "provider_access_denied"

// restrictProvidersForClient drops the providers the requesting client API key may not use
// (config: provider-allowed-api-keys). providers must already be normalized. A request without a
// client API key is treated like an unlisted key. It returns an error when no provider is left.
func (m *Manager) restrictProvidersForClient(ctx context.Context, providers []string) ([]string, error) {
	cfg := m.runtimeConfigSnapshot()
	if cfg == nil || len(cfg.ProviderAllowedAPIKeys) == 0 {
		return providers, nil
	}
	apiKey := coreusage.APIKeyFromContext(ctx)
	permitted := func(provider string) bool {
		allowed, restricted := cfg.ProviderAllowedAPIKeys[provider]
		return !restricted || (apiKey != "" && slices.Contains(allowed, apiKey))
	}
	denied := slices.IndexFunc(providers, func(provider string) bool { return !permitted(provider) })
	if denied < 0 {
		return providers, nil
	}
	kept := make([]string, 0, len(providers)-1)
	kept = append(kept, providers[:denied]...)
	for _, provider := range providers[denied+1:] {
		if permitted(provider) {
			kept = append(kept, provider)
		}
	}
	if len(kept) == 0 {
		return nil, &Error{
			Code:       ErrorCodeProviderAccessDenied,
			Message:    "this API key is not allowed to use the requested model",
			HTTPStatus: http.StatusForbidden,
		}
	}
	return kept, nil
}
