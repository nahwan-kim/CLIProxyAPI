package auth

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

type providerAccessTestExecutor struct {
	provider string
	calls    atomic.Int32
}

func (e *providerAccessTestExecutor) Identifier() string { return e.provider }

func (e *providerAccessTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls.Add(1)
	return cliproxyexecutor.Response{Payload: []byte(e.provider)}, nil
}

func (e *providerAccessTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls.Add(1)
	return cliproxyexecutor.Response{Payload: []byte(e.provider)}, nil
}

func (e *providerAccessTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.calls.Add(1)
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(e.provider)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*providerAccessTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*providerAccessTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

type providerAccessFixture struct {
	manager   *Manager
	model     string
	executors map[string]*providerAccessTestExecutor
	authIDs   map[string]string
}

// newProviderAccessFixture registers one credential per provider, all serving the same model,
// and applies the given provider-allowed-api-keys restriction.
func newProviderAccessFixture(t *testing.T, allowed map[string][]string, providers ...string) *providerAccessFixture {
	t.Helper()
	fixture := &providerAccessFixture{
		manager:   NewManager(nil, nil, NoopHook{}),
		model:     "provider-access-model-" + uuid.NewString(),
		executors: make(map[string]*providerAccessTestExecutor, len(providers)),
		authIDs:   make(map[string]string, len(providers)),
	}
	fixture.manager.SetRetryConfig(0, 0, 0)
	cfg := &internalconfig.Config{ProviderAllowedAPIKeys: allowed}
	cfg.SanitizeProviderAllowedAPIKeys()
	fixture.manager.SetConfig(cfg)
	for _, provider := range providers {
		executor := &providerAccessTestExecutor{provider: provider}
		fixture.executors[provider] = executor
		fixture.manager.RegisterExecutor(executor)
		auth := &Auth{
			ID:       "provider-access-auth-" + uuid.NewString(),
			Provider: provider,
			Metadata: map[string]any{"request_retry": float64(0)},
		}
		fixture.authIDs[provider] = auth.ID
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: fixture.model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
		if _, errRegister := fixture.manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", provider, errRegister)
		}
	}
	return fixture
}

type providerAccessCall struct {
	name string
	run  func(ctx context.Context, fixture *providerAccessFixture, providers []string, opts cliproxyexecutor.Options) error
}

func providerAccessCalls() []providerAccessCall {
	return []providerAccessCall{
		{name: "execute", run: func(ctx context.Context, fixture *providerAccessFixture, providers []string, opts cliproxyexecutor.Options) error {
			_, errExecute := fixture.manager.Execute(ctx, providers, cliproxyexecutor.Request{Model: fixture.model}, opts)
			return errExecute
		}},
		{name: "count tokens", run: func(ctx context.Context, fixture *providerAccessFixture, providers []string, opts cliproxyexecutor.Options) error {
			_, errCount := fixture.manager.ExecuteCount(ctx, providers, cliproxyexecutor.Request{Model: fixture.model}, opts)
			return errCount
		}},
		{name: "stream", run: func(ctx context.Context, fixture *providerAccessFixture, providers []string, opts cliproxyexecutor.Options) error {
			opts.Stream = true
			result, errStream := fixture.manager.ExecuteStream(ctx, providers, cliproxyexecutor.Request{Model: fixture.model}, opts)
			if errStream != nil {
				return errStream
			}
			for range result.Chunks {
			}
			return nil
		}},
	}
}

func requireProviderAccessDenied(t *testing.T, err error) {
	t.Helper()
	var authErr *Error
	if !errors.As(err, &authErr) || authErr.Code != ErrorCodeProviderAccessDenied {
		t.Fatalf("error = %v, want %s", err, ErrorCodeProviderAccessDenied)
	}
	if authErr.StatusCode() != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", authErr.StatusCode(), http.StatusForbidden)
	}
}

func TestProviderAllowedAPIKeysServesOnlyListedClientKeys(t *testing.T) {
	const ownerKey, guestKey = "owner-key", "guest-key"
	clients := []struct {
		name    string
		ctx     context.Context
		allowed bool
	}{
		{name: "listed key", ctx: coreusage.WithAPIKey(context.Background(), ownerKey), allowed: true},
		{name: "unlisted key", ctx: coreusage.WithAPIKey(context.Background(), guestKey)},
		{name: "no client key", ctx: context.Background()},
	}
	for _, call := range providerAccessCalls() {
		for _, client := range clients {
			t.Run(call.name+"/"+client.name, func(t *testing.T) {
				// The config spells the provider in mixed case with padded keys on purpose.
				fixture := newProviderAccessFixture(t, map[string][]string{" Claude ": {" " + ownerKey + " "}}, "claude")
				err := call.run(client.ctx, fixture, []string{"claude"}, cliproxyexecutor.Options{})
				calls := fixture.executors["claude"].calls.Load()
				if client.allowed {
					if err != nil || calls != 1 {
						t.Fatalf("err = %v, upstream calls = %d; want success with one upstream call", err, calls)
					}
					return
				}
				requireProviderAccessDenied(t, err)
				if calls != 0 {
					t.Fatalf("upstream calls = %d, want 0", calls)
				}
			})
		}
	}
}

func TestProviderAllowedAPIKeysEmptyListClosesProvider(t *testing.T) {
	fixture := newProviderAccessFixture(t, map[string][]string{"claude": nil}, "claude")
	ctx := coreusage.WithAPIKey(context.Background(), "any-key")
	_, err := fixture.manager.Execute(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: fixture.model}, cliproxyexecutor.Options{})
	requireProviderAccessDenied(t, err)
}

func TestProviderAllowedAPIKeysLeavesOtherProvidersOpen(t *testing.T) {
	for _, call := range providerAccessCalls() {
		t.Run(call.name, func(t *testing.T) {
			fixture := newProviderAccessFixture(t, map[string][]string{"claude": {"owner-key"}}, "claude", "codex")
			ctx := coreusage.WithAPIKey(context.Background(), "guest-key")
			if err := call.run(ctx, fixture, []string{"codex"}, cliproxyexecutor.Options{}); err != nil {
				t.Fatalf("unrestricted provider error = %v", err)
			}
			// A model served by both providers falls through to the open one for a guest key.
			if err := call.run(ctx, fixture, []string{"claude", "codex"}, cliproxyexecutor.Options{}); err != nil {
				t.Fatalf("mixed providers error = %v", err)
			}
			if calls := fixture.executors["claude"].calls.Load(); calls != 0 {
				t.Fatalf("restricted upstream calls = %d, want 0", calls)
			}
			if calls := fixture.executors["codex"].calls.Load(); calls != 2 {
				t.Fatalf("open upstream calls = %d, want 2", calls)
			}
		})
	}
}

func TestProviderAllowedAPIKeysIgnoresPinnedRestrictedAuth(t *testing.T) {
	for _, call := range providerAccessCalls() {
		t.Run(call.name, func(t *testing.T) {
			fixture := newProviderAccessFixture(t, map[string][]string{"claude": {"owner-key"}}, "claude", "codex")
			ctx := coreusage.WithAPIKey(context.Background(), "guest-key")
			opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: fixture.authIDs["claude"]}}
			if err := call.run(ctx, fixture, []string{"claude", "codex"}, opts); err == nil {
				t.Fatal("pinned restricted credential was served to a guest key")
			}
			if calls := fixture.executors["claude"].calls.Load(); calls != 0 {
				t.Fatalf("restricted upstream calls = %d, want 0", calls)
			}
		})
	}
}

func TestProviderAllowedAPIKeysMatchesProviderSpellings(t *testing.T) {
	// An entry and a request can spell one provider differently: scheduling treats kimi.com as
	// kimi, and a config that was not sanitized on load keeps its original case and padding.
	for _, tc := range []struct{ entry, requested string }{
		{entry: "kimi", requested: "kimi.com"},
		{entry: "kimi.com", requested: "kimi"},
		{entry: "kimi-ai", requested: "kimi.ai"},
		{entry: " Claude ", requested: "claude"},
	} {
		t.Run(tc.entry+"/"+tc.requested, func(t *testing.T) {
			manager := NewManager(nil, nil, NoopHook{})
			manager.SetConfig(&internalconfig.Config{ProviderAllowedAPIKeys: map[string][]string{tc.entry: {"owner-key"}}})
			guest := coreusage.WithAPIKey(context.Background(), "guest-key")
			_, err := manager.restrictProvidersForClient(guest, []string{tc.requested})
			requireProviderAccessDenied(t, err)
			owner := coreusage.WithAPIKey(context.Background(), "owner-key")
			kept, err := manager.restrictProvidersForClient(owner, []string{tc.requested})
			if err != nil || len(kept) != 1 || kept[0] != tc.requested {
				t.Fatalf("listed key: providers = %v, err = %v; want [%s]", kept, err, tc.requested)
			}
		})
	}
}

func TestProviderAllowedAPIKeysGuardsDirectCredentialSelection(t *testing.T) {
	fixture := newProviderAccessFixture(t, map[string][]string{"claude": {"owner-key"}}, "claude")
	guest := coreusage.WithAPIKey(context.Background(), "guest-key")
	opts := cliproxyexecutor.Options{}

	_, err := fixture.manager.SelectAuth(guest, "claude", fixture.model, opts)
	requireProviderAccessDenied(t, err)
	_, err = fixture.manager.SelectAuthByKind(guest, "claude", fixture.model, AuthKindOAuth, opts)
	requireProviderAccessDenied(t, err)
	_, err = fixture.manager.SelectAuthWithCredentialPolicy(guest, "claude", fixture.model, CredentialPolicyCodexAlphaSearchV1, opts)
	requireProviderAccessDenied(t, err)

	owner := coreusage.WithAPIKey(context.Background(), "owner-key")
	selected, err := fixture.manager.SelectAuth(owner, "claude", fixture.model, opts)
	if err != nil || selected == nil || selected.ID != fixture.authIDs["claude"] {
		t.Fatalf("listed key: selected = %v, err = %v; want the claude credential", selected, err)
	}
}

func TestProviderAllowedAPIKeysGuardsHomeDispatchedProvider(t *testing.T) {
	for _, client := range []struct {
		name    string
		key     string
		allowed bool
	}{
		{name: "listed key", key: "owner-key", allowed: true},
		{name: "unlisted key", key: "guest-key"},
	} {
		t.Run(client.name, func(t *testing.T) {
			// Home names the provider only after dispatch, so the request-time provider list
			// cannot carry the restriction.
			dispatcher := &fixtureHomeDispatcher{payload: []byte(`{"auth":{"id":"home-claude-auth","provider":"claude"}}`)}
			manager := newHomeSelectionTestManager(t, dispatcher)
			manager.RegisterExecutor(schedulerTestExecutor{provider: "claude"})
			manager.SetConfig(&internalconfig.Config{ProviderAllowedAPIKeys: map[string][]string{"claude": {"owner-key"}}})
			opts := cliproxyexecutor.Options{}
			opts.EnsureMetadata()
			selection, err := manager.pickHomeDispatchSelection(coreusage.WithAPIKey(t.Context(), client.key), "model", opts)
			if client.allowed {
				if err != nil {
					t.Fatalf("listed key: err = %v", err)
				}
				selection.End("test_complete")
				return
			}
			requireProviderAccessDenied(t, err)
			if selection != nil {
				t.Fatal("a restricted Home credential was selected for a guest key")
			}
		})
	}
}

func TestProviderAllowedAPIKeysRevokesRetainedHomeSession(t *testing.T) {
	// A websocket session keeps its Home credential between turns, so a restriction added
	// mid-session must still apply to the next turn.
	dispatcher := &retainingHomeExecutionDispatcher{}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	executor := &retainingHomeExecutionExecutor{}
	manager.RegisterExecutor(executor)

	ctx := coreusage.WithAPIKey(cliproxyexecutor.WithDownstreamWebsocket(context.Background()), "guest-key")
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey: "session-revoked",
		cliproxyexecutor.PinnedAuthMetadataKey:       "home-auth",
	}}
	request := cliproxyexecutor.Request{Model: "model-a"}
	// Home mode hands Execute the placeholder provider list, as the request handlers do.
	if _, errExecute := manager.Execute(ctx, []string{"home"}, request, opts); errExecute != nil {
		t.Fatalf("Execute() before the restriction: %v", errExecute)
	}
	manager.SetConfig(&internalconfig.Config{
		Home:                   internalconfig.HomeConfig{Enabled: true},
		ProviderAllowedAPIKeys: map[string][]string{executor.Identifier(): {"owner-key"}},
	})
	_, errExecute := manager.Execute(ctx, []string{"home"}, request, opts)
	requireProviderAccessDenied(t, errExecute)
	if got := executor.calls.Load(); got != 1 {
		t.Fatalf("executor calls = %d, want 1: the retained credential served a revoked key", got)
	}
}

func TestProviderAllowedAPIKeysGuardsHomeCredentialSchedulingKey(t *testing.T) {
	// An OpenAI-compatible Home credential carries a logical provider label that differs from
	// the key its executor is scheduled under. A restriction on either name must apply.
	const payload = `{"auth":{"id":"home-pool-auth","provider":"home-pool","attributes":{"api_key":"test-key","compat_name":"pool","provider_key":"pool"}}}`
	for _, restricted := range []string{"home-pool", "openai-compatible-pool"} {
		t.Run(restricted, func(t *testing.T) {
			manager := newHomeSelectionTestManager(t, &fixtureHomeDispatcher{payload: []byte(payload)})
			manager.RegisterExecutor(schedulerTestExecutor{provider: "openai-compatible-pool"})
			manager.SetConfig(&internalconfig.Config{ProviderAllowedAPIKeys: map[string][]string{restricted: {"owner-key"}}})
			opts := cliproxyexecutor.Options{}
			opts.EnsureMetadata()
			selection, err := manager.pickHomeDispatchSelection(coreusage.WithAPIKey(t.Context(), "guest-key"), "model", opts)
			requireProviderAccessDenied(t, err)
			if selection != nil {
				t.Fatal("a restricted Home credential was selected for a guest key")
			}
			selection, err = manager.pickHomeDispatchSelection(coreusage.WithAPIKey(t.Context(), "owner-key"), "model", opts)
			if err != nil {
				t.Fatalf("listed key: err = %v", err)
			}
			selection.End("test_complete")
		})
	}
}
