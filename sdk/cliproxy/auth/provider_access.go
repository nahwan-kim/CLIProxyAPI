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

func providerAccessDeniedError() *Error {
	return &Error{
		Code:       ErrorCodeProviderAccessDenied,
		Message:    "this API key is not allowed to use the requested model",
		HTTPStatus: http.StatusForbidden,
	}
}

// clientMayUseProvider reports whether apiKey may use provider under restrictions (config:
// provider-allowed-api-keys). Names are compared in their scheduling form, so an alias such as
// kimi.com and an entry that was not normalized on load still match the provider they name. An
// empty apiKey is treated like an unlisted key.
func clientMayUseProvider(restrictions map[string][]string, provider, apiKey string) bool {
	canonical := canonicalSchedulingProvider(provider)
	restricted := false
	for name, allowed := range restrictions {
		if canonicalSchedulingProvider(name) != canonical {
			continue
		}
		if apiKey != "" && slices.Contains(allowed, apiKey) {
			return true
		}
		restricted = true
	}
	return !restricted
}

// checkProviderAccess rejects a request whose client API key may not use every named provider.
// It guards the paths that pick a credential for a known provider: direct selection and Home
// dispatch. Home passes both names a credential answers to, its logical provider and the key its
// executor is scheduled under, which differ for an OpenAI-compatible pool.
func (m *Manager) checkProviderAccess(ctx context.Context, providers ...string) error {
	cfg := m.runtimeConfigSnapshot()
	if cfg == nil || len(cfg.ProviderAllowedAPIKeys) == 0 {
		return nil
	}
	apiKey := coreusage.APIKeyFromContext(ctx)
	for _, provider := range providers {
		if !clientMayUseProvider(cfg.ProviderAllowedAPIKeys, provider, apiKey) {
			return providerAccessDeniedError()
		}
	}
	return nil
}

// checkHomeSelectionAccess applies checkProviderAccess to a Home credential that is already
// selected, such as one a websocket session retains between turns.
func (m *Manager) checkHomeSelectionAccess(ctx context.Context, selection *HomeDispatchSelection) error {
	cfg := m.runtimeConfigSnapshot()
	if cfg == nil || len(cfg.ProviderAllowedAPIKeys) == 0 {
		return nil
	}
	selection.authMu.RLock()
	executorKey := executorKeyFromAuth(selection.Auth)
	selection.authMu.RUnlock()
	return m.checkProviderAccess(ctx, selection.Provider, executorKey)
}

// restrictProvidersForClient drops the providers the requesting client API key may not use. It
// returns an error when no provider is left.
func (m *Manager) restrictProvidersForClient(ctx context.Context, providers []string) ([]string, error) {
	cfg := m.runtimeConfigSnapshot()
	if cfg == nil || len(cfg.ProviderAllowedAPIKeys) == 0 {
		return providers, nil
	}
	apiKey := coreusage.APIKeyFromContext(ctx)
	permitted := func(provider string) bool {
		return clientMayUseProvider(cfg.ProviderAllowedAPIKeys, provider, apiKey)
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
		return nil, providerAccessDeniedError()
	}
	return kept, nil
}
