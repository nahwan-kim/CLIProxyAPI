package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func newTypeSafeTestServer(t *testing.T, typeSafe proxyconfig.TypeSafeConfig) *Server {
	t.Helper()
	return newTestServerWithConfig(t, &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{APIKeys: []string{"test-key"}},
		TypeSafe:  typeSafe,
	})
}

func TestTypeSafeSystemOneRelaysWithConfiguredKey(t *testing.T) {
	const requestBody = `{"model":"jev-1.14.0","state":"s","questions":{"q":{"type":"noul","instructions":"i"}}}`
	var gotPath, gotAuth, gotImages, gotUnrelated, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotImages = r.Header.Get("TypeSafe-Danger-Zone-Images")
		gotUnrelated = r.Header.Get("X-Unrelated")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"bad question"}`))
	}))
	defer upstream.Close()

	server := newTypeSafeTestServer(t, proxyconfig.TypeSafeConfig{APIKey: "ts-key", BaseURL: upstream.URL + "/"})
	req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(requestBody))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("TypeSafe-Danger-Zone-Images", "true")
	req.Header.Set("X-Unrelated", "drop-me")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)

	if gotPath != "/v1/systemone" {
		t.Fatalf("upstream path = %q, want /v1/systemone", gotPath)
	}
	if gotAuth != "Bearer ts-key" {
		t.Fatalf("upstream Authorization = %q, want the configured TypeSafe key", gotAuth)
	}
	if gotImages != "true" {
		t.Fatalf("TypeSafe-Danger-Zone-Images = %q, want forwarded", gotImages)
	}
	if gotUnrelated != "" {
		t.Fatalf("X-Unrelated = %q, want not forwarded", gotUnrelated)
	}
	if gotBody != requestBody {
		t.Fatalf("upstream body = %q, want %q", gotBody, requestBody)
	}
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want upstream 422", recorder.Code)
	}
	if got := recorder.Body.String(); got != `{"error":"bad question"}` {
		t.Fatalf("body = %q, want upstream body", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
}

func TestTypeSafeSystemOneRequiresProxyKey(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer upstream.Close()

	server := newTypeSafeTestServer(t, proxyconfig.TypeSafeConfig{APIKey: "ts-key", BaseURL: upstream.URL})
	req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer ts-key")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a non-proxy key", recorder.Code)
	}
	if called {
		t.Fatal("upstream was called without a valid proxy key")
	}
}

func TestTypeSafeSystemOneDisabledWithoutKey(t *testing.T) {
	server := newTypeSafeTestServer(t, proxyconfig.TypeSafeConfig{})
	req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer test-key")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when TypeSafe is not configured", recorder.Code)
	}
}
