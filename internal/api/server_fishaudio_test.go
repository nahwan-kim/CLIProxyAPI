package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func newFishAudioTestServer(t *testing.T, fishAudio proxyconfig.FishAudioConfig) *Server {
	t.Helper()
	return newTestServerWithConfig(t, &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{APIKeys: []string{"test-key"}},
		FishAudio: fishAudio,
	})
}

func TestFishAudioTTSRelaysBinaryBodyWithConfiguredKey(t *testing.T) {
	// MessagePack is not valid UTF-8 or JSON; the relay must not touch a byte of it.
	requestBody := []byte{0x82, 0xa4, 't', 'e', 'x', 't', 0xa2, 'h', 'i', 0xa5, 'a', 'u', 'd', 'i', 'o', 0xc4, 0x04, 0x00, 0xff, 0xfe, 0x80}
	responseBody := []byte{0xff, 0xfb, 0x90, 0x00, 0x01}
	var gotPath, gotAuth, gotModel, gotType, gotUnrelated string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotModel = r.Header.Get("model")
		gotType = r.Header.Get("Content-Type")
		gotUnrelated = r.Header.Get("X-Unrelated")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(responseBody)
	}))
	defer upstream.Close()

	server := newFishAudioTestServer(t, proxyconfig.FishAudioConfig{APIKey: "fish-key", BaseURL: upstream.URL + "/"})
	req := httptest.NewRequest(http.MethodPost, "/fish/v1/tts", bytes.NewReader(requestBody))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/msgpack")
	req.Header.Set("model", "s2.1-pro-free")
	req.Header.Set("X-Unrelated", "drop-me")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)

	if gotPath != "/v1/tts" {
		t.Fatalf("upstream path = %q, want /v1/tts", gotPath)
	}
	if gotAuth != "Bearer fish-key" {
		t.Fatalf("upstream Authorization = %q, want the configured Fish Audio key", gotAuth)
	}
	if gotModel != "s2.1-pro-free" {
		t.Fatalf("upstream model header = %q, want forwarded", gotModel)
	}
	if gotType != "application/msgpack" {
		t.Fatalf("upstream Content-Type = %q, want application/msgpack", gotType)
	}
	if gotUnrelated != "" {
		t.Fatalf("X-Unrelated = %q, want not forwarded", gotUnrelated)
	}
	if !bytes.Equal(gotBody, requestBody) {
		t.Fatalf("upstream body = %x, want %x", gotBody, requestBody)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want upstream 200", recorder.Code)
	}
	if !bytes.Equal(recorder.Body.Bytes(), responseBody) {
		t.Fatalf("body = %x, want upstream audio %x", recorder.Body.Bytes(), responseBody)
	}
	if got := recorder.Header().Get("Content-Type"); got != "audio/mpeg" {
		t.Fatalf("Content-Type = %q, want audio/mpeg", got)
	}
}

func TestFishAudioTTSRelaysUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"message":"no credit"}`))
	}))
	defer upstream.Close()

	server := newFishAudioTestServer(t, proxyconfig.FishAudioConfig{APIKey: "fish-key", BaseURL: upstream.URL})
	req := httptest.NewRequest(http.MethodPost, "/fish/v1/tts", strings.NewReader(`{"text":"hi"}`))
	req.Header.Set("Authorization", "Bearer test-key")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want upstream 402", recorder.Code)
	}
	if got := recorder.Body.String(); got != `{"message":"no credit"}` {
		t.Fatalf("body = %q, want upstream body", got)
	}
}

func TestFishAudioTTSRequiresProxyKey(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer upstream.Close()

	server := newFishAudioTestServer(t, proxyconfig.FishAudioConfig{APIKey: "fish-key", BaseURL: upstream.URL})
	req := httptest.NewRequest(http.MethodPost, "/fish/v1/tts", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer fish-key")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a non-proxy key", recorder.Code)
	}
	if called {
		t.Fatal("upstream was called without a valid proxy key")
	}
}

func TestFishAudioTTSDisabledWithoutKey(t *testing.T) {
	server := newFishAudioTestServer(t, proxyconfig.FishAudioConfig{})
	req := httptest.NewRequest(http.MethodPost, "/fish/v1/tts", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer test-key")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when Fish Audio is not configured", recorder.Code)
	}
}

func TestFishAudioTTSRejectsOversizedBody(t *testing.T) {
	for _, tc := range []struct {
		name          string
		contentLength int64
	}{
		{name: "declared length", contentLength: maxFishAudioTTSBodyBytes + 1},
		{name: "chunked", contentLength: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "audio/mpeg")
			}))
			defer upstream.Close()

			server := newFishAudioTestServer(t, proxyconfig.FishAudioConfig{APIKey: "fish-key", BaseURL: upstream.URL})
			req := httptest.NewRequest(http.MethodPost, "/fish/v1/tts", io.LimitReader(zeroReader{}, maxFishAudioTTSBodyBytes+1))
			req.ContentLength = tc.contentLength
			req.Header.Set("Authorization", "Bearer test-key")
			req.Header.Set("Content-Type", "application/msgpack")
			recorder := httptest.NewRecorder()
			server.engine.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413 for a body over the limit", recorder.Code)
			}
		})
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
