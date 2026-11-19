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
