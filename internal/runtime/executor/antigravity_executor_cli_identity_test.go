package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// The tools mirror an omp client: read carries a required intent field, and grep
// requires arguments the CLI's grep_search cannot supply.
const antigravityCLIIdentityTestPayload = `{
	"request": {
		"systemInstruction": {"parts": [{"text": "⁂ Role\nYou are omp's trusted coding assistant.\nKeep omp paths."}]},
		"generationConfig": {"temperature": 0.7, "topP": 0.9},
		"contents": [
			{"role": "user", "parts": [{"text": "list files"}]},
			{"role": "model", "parts": [
				{"functionCall": {"id": "c1", "name": "bash", "args": {"command": "ls"}}},
				{"functionCall": {"id": "c2", "name": "read", "args": {"path": "src/a.go:raw:3-9", "i": "Reading a"}}},
				{"functionCall": {"id": "c3", "name": "read", "args": {"path": "https://example.com", "i": "Reading page"}}}
			]},
			{"role": "user", "parts": [
				{"functionResponse": {"id": "c1", "name": "bash", "response": {"output": "a"}}},
				{"functionResponse": {"id": "c2", "name": "read", "response": {"output": "b"}}},
				{"functionResponse": {"id": "c3", "name": "read", "response": {"output": "c"}}}
			]}
		],
		"tools": [{"functionDeclarations": [
			{"name": "bash", "parameters": {"type": "object"}},
			{"name": "read", "description": "Read", "parameters": {"type": "object", "properties": {"path": {"type": "string"}, "i": {"type": "string"}}, "required": ["path", "i"]}},
			{"name": "grep", "parameters": {"type": "object", "properties": {"pattern": {"type": "string"}, "path": {"type": "string"}, "skip": {"type": "number"}}, "required": ["pattern", "path", "skip"]}}
		]}]
	}
}`

func TestAntigravityBuildRequestCLIIdentity(t *testing.T) {
	var (
		upstreamBody     []byte
		transferEncoding []string
		userAgent        string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamBody, _ = io.ReadAll(r.Body)
		transferEncoding = r.TransferEncoding
		userAgent = r.Header.Get("User-Agent")
	}))
	defer server.Close()

	const model = "gemini-3-flash"
	// A user payload rule restores a field the CLI shape drops: rules must run last.
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: model}},
		Params: map[string]any{"request.generationConfig.temperature": 0.3},
	}}}}
	original := []byte(antigravityCLIIdentityTestPayload)
	req := cliproxyexecutor.Request{Model: model, Payload: original}
	ctx := helps.WithPayloadFinalizer(context.Background(), helps.NewPayloadFinalizer(cfg, "antigravity", model, "antigravity", "", original, req, cliproxyexecutor.Options{}))

	auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "project-1", "client_identity": "cli"}}
	executor := &AntigravityExecutor{}
	httpReq, localPayload, err := executor.buildRequestDoc(ctx, auth, "token", model, ojson.NewDoc(original), true, "", server.URL)
	if err != nil {
		t.Fatalf("buildRequestDoc error: %v", err)
	}
	httpResp, err := server.Client().Do(httpReq)
	if err != nil {
		t.Fatalf("send request error: %v", err)
	}
	_ = httpResp.Body.Close()

	if userAgent != helps.AntigravityCLIUserAgent {
		t.Errorf("User-Agent = %q, want CLI identity", userAgent)
	}
	if len(transferEncoding) != 1 || transferEncoding[0] != "chunked" {
		t.Errorf("Transfer-Encoding = %v, want chunked", transferEncoding)
	}

	sent := ojson.ParseBytes(upstreamBody)
	expect := func(doc ojson.Result, path, want string) {
		t.Helper()
		if got := doc.Get(path).String(); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	if got := sent.Get("request.generationConfig.temperature").Float(); got != 0.3 {
		t.Errorf("temperature = %v, want the user payload rule's 0.3", got)
	}
	if sent.Get("request.generationConfig.topP").Exists() {
		t.Errorf("topP survived; the CLI sends no sampling parameters")
	}
	expect(sent, "request.sessionId", "-3750763034362895579")
	labels := sent.Get("request.labels")
	cascade, trajectory := labels.Get("cascade_id").String(), labels.Get("trajectory_id").String()
	if cascade == "" || trajectory == "" || cascade == trajectory || labels.Get("root_cascade_id").String() != cascade {
		t.Fatalf("labels = %s, want distinct cascade/trajectory ids and a matching root", labels.Raw)
	}
	// Three contents = step 3; one model turn already answered = request index 1.
	expect(labels, "last_step_index", "2")
	expect(labels, "request_id", trajectory+"-1")
	parts := strings.Split(sent.Get("requestId").String(), "/")
	if len(parts) != 5 || parts[0] != "agent" || parts[1] != cascade || parts[3] != trajectory || parts[4] != "3" {
		t.Errorf("requestId = %q, want agent/<cascade>/<ts>/<trajectory>/3", sent.Get("requestId").String())
	}
	expect(sent, "request.systemInstruction.parts.0.text", "⁂ Role\nYou are Antigravity's trusted coding assistant.\nKeep omp paths.")

	// One entry per declaration: bash under its CLI name, read_file ahead of the
	// client's read, and grep alone because its schema rules out grep_search.
	var declared []string
	for _, tool := range sent.Get("request.tools").Array() {
		if got := tool.Get("functionDeclarations.#").Int(); got != 1 {
			t.Errorf("tools entry holds %d declarations, want 1", got)
		}
		declared = append(declared, tool.Get("functionDeclarations.0.name").String())
	}
	if got, want := strings.Join(declared, ","), "run_command,read_file,read,grep"; got != want {
		t.Fatalf("declared tools = %s, want %s", got, want)
	}
	expect(sent, "request.tools.1.functionDeclarations.0.parameters.properties.i.type", "string")
	if got := sent.Get("request.tools.1.functionDeclarations.0.parameters.required").Raw; got != `["Path","i"]` {
		t.Errorf("read_file required = %s, want Path and the client's intent field", got)
	}

	// Replayed history: an expressible read becomes read_file, a URL read stays
	// the client's tool, and every result follows its call.
	call := "request.contents.1.parts."
	expect(sent, call+"0.functionCall.name", "run_command")
	expect(sent, call+"1.functionCall.name", "read_file")
	// The upstream agent serializer emits call arguments in sorted key order.
	if got, want := sent.Get(call+"1.functionCall.args").Raw, `{"EndLine":9,"Path":"src/a.go","StartLine":3,"i":"Reading a"}`; got != want {
		t.Errorf("read_file args = %s, want %s", got, want)
	}
	expect(sent, call+"2.functionCall.name", "read")
	expect(sent, call+"2.functionCall.args.path", "https://example.com")
	result := "request.contents.2.parts."
	expect(sent, result+"0.functionResponse.name", "run_command")
	expect(sent, result+"1.functionResponse.name", "read_file")
	expect(sent, result+"2.functionResponse.name", "read")

	// Translators and the replay cache must keep seeing what the client sent.
	local := ojson.ParseBytes(localPayload)
	var localDeclared []string
	for _, tool := range local.Get("request.tools").Array() {
		localDeclared = append(localDeclared, tool.Get("functionDeclarations.0.name").String())
	}
	if got, want := strings.Join(localDeclared, ","), "bash,read,grep"; got != want {
		t.Errorf("local declared tools = %s, want the client's %s", got, want)
	}
	expect(local, call+"0.functionCall.name", "bash")
	expect(local, call+"1.functionCall.name", "read")
	expect(local, call+"1.functionCall.args.path", "src/a.go:raw:3-9")
	expect(local, result+"1.functionResponse.name", "read")

	restorer := helps.AntigravityCLIToolRoutesFrom(httpReq.Context()).NewResponseRestorer()
	restore := func(functionCall string) ojson.Result {
		line := []byte(`data: {"response":{"candidates":[{"content":{"parts":[{"functionCall":` + functionCall + `}]}}]}}`)
		return ojson.ParseBytes(helps.JSONPayload(restorer.Restore(line))).Get("response.candidates.0.content.parts.0.functionCall")
	}
	expect(restore(`{"name":"run_command","args":{"command":"pwd"}}`), "name", "bash")
	read := restore(`{"name":"read_file","args":{"Path":"go.mod","EndLine":4,"i":"Reading module"}}`)
	expect(read, "name", "read")
	if got, want := read.Get("args").Raw, `{"path":"go.mod:raw:1-4","i":"Reading module"}`; got != want {
		t.Errorf("restored read args = %s, want %s", got, want)
	}
	// Arguments outside the contract must not reach the client's read tool at all.
	expect(restore(`{"name":"read_file","args":{"Path":"../secret","i":"Reading"}}`), "name", "read_file")
}

func TestAntigravityCLIIdentityPayloadRuleReplacingToolsIsNotOverwritten(t *testing.T) {
	const model = "gemini-3-flash"
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: model}},
		Params: map[string]any{"request.tools": []any{map[string]any{"functionDeclarations": []any{map[string]any{"name": "custom"}}}}},
	}}}}
	original := []byte(antigravityCLIIdentityTestPayload)
	req := cliproxyexecutor.Request{Model: model, Payload: original}
	ctx := helps.WithPayloadFinalizer(context.Background(), helps.NewPayloadFinalizer(cfg, "antigravity", model, "antigravity", "", original, req, cliproxyexecutor.Options{}))
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "project-1", "client_identity": "cli"}}

	httpReq, localPayload, err := (&AntigravityExecutor{}).buildRequestDoc(ctx, auth, "token", model, ojson.NewDoc(original), true, "", "https://example.com")
	if err != nil {
		t.Fatalf("buildRequestDoc error: %v", err)
	}
	wire, _ := io.ReadAll(httpReq.Body)
	for view, payload := range map[string][]byte{"upstream": wire, "local": localPayload} {
		tools := ojson.ParseBytes(payload).Get("request.tools")
		if tools.Get("#").Int() != 1 || tools.Get("0.functionDeclarations.0.name").String() != "custom" {
			t.Errorf("%s tools = %s, want only the rule's tool", view, tools.Raw)
		}
	}
	// Neither run_command nor read_file is declared any more, so no response call
	// may be mapped onto a client tool.
	if routes := helps.AntigravityCLIToolRoutesFrom(httpReq.Context()); routes.NewResponseRestorer() != nil {
		t.Errorf("response routes survived a rule that removed their declarations")
	}
}

// A rule that changes what one generated declaration means keeps that declaration
// and ends its route, without disturbing the other tools.
func TestAntigravityCLIIdentityPayloadRuleRedefiningOneToolEndsOnlyItsRoute(t *testing.T) {
	const model = "gemini-3-flash"
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: model}},
		Params: map[string]any{"request.tools.0.functionDeclarations.0.description": "Record a command without running it"},
	}}}}
	original := []byte(antigravityCLIIdentityTestPayload)
	req := cliproxyexecutor.Request{Model: model, Payload: original}
	ctx := helps.WithPayloadFinalizer(context.Background(), helps.NewPayloadFinalizer(cfg, "antigravity", model, "antigravity", "", original, req, cliproxyexecutor.Options{}))
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "project-1", "client_identity": "cli"}}

	httpReq, localPayload, err := (&AntigravityExecutor{}).buildRequestDoc(ctx, auth, "token", model, ojson.NewDoc(original), true, "", "https://example.com")
	if err != nil {
		t.Fatalf("buildRequestDoc error: %v", err)
	}
	var localDeclared []string
	for _, tool := range ojson.ParseBytes(localPayload).Get("request.tools").Array() {
		localDeclared = append(localDeclared, tool.Get("functionDeclarations.0.name").String())
	}
	if got, want := strings.Join(localDeclared, ","), "run_command,read,grep"; got != want {
		t.Fatalf("local declared tools = %s, want %s", got, want)
	}
	restorer := helps.AntigravityCLIToolRoutesFrom(httpReq.Context()).NewResponseRestorer()
	restored := ojson.ParseBytes(restorer.Restore([]byte(`{"response":{"candidates":[{"content":{"parts":[
		{"functionCall":{"name":"run_command","args":{"command":"pwd"}}},
		{"functionCall":{"name":"read_file","args":{"Path":"go.mod","i":"Reading"}}}]}}]}}`))).Get("response.candidates.0.content.parts")
	if got, want := restored.Get("0.functionCall.name").String()+","+restored.Get("1.functionCall.name").String(), "run_command,read"; got != want {
		t.Fatalf("restored calls = %s, want %s", got, want)
	}
}

func TestAntigravityCLIIdentityBodySurvivesRedirect(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		if len(bodies) == 1 {
			http.Redirect(w, r, r.URL.Path+"?redirected=1", http.StatusTemporaryRedirect)
		}
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "project-1", "client_identity": "cli"}}
	httpReq, _, err := (&AntigravityExecutor{}).buildRequestDoc(context.Background(), auth, "token", "gemini-3-flash", ojson.NewDoc([]byte(antigravityCLIIdentityTestPayload)), true, "", server.URL)
	if err != nil {
		t.Fatalf("buildRequestDoc error: %v", err)
	}
	httpResp, err := server.Client().Do(httpReq)
	if err != nil {
		t.Fatalf("send request error: %v", err)
	}
	_ = httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK || len(bodies) != 2 || bodies[0] == "" || bodies[0] != bodies[1] {
		t.Fatalf("status=%d with %d attempts; want the same body replayed after the redirect", httpResp.StatusCode, len(bodies))
	}
}

func TestAntigravityBuildRequestWithoutCLIIdentityKeepsHubShape(t *testing.T) {
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "project-1"}}
	executor := &AntigravityExecutor{}
	httpReq, _, err := executor.buildRequestDoc(context.Background(), auth, "token", "gemini-3-flash", ojson.NewDoc([]byte(antigravityCLIIdentityTestPayload)), true, "", "https://example.com")
	if err != nil {
		t.Fatalf("buildRequestDoc error: %v", err)
	}
	raw, _ := io.ReadAll(httpReq.Body)
	sent := ojson.ParseBytes(raw)
	if got := sent.Get("request.tools.0.functionDeclarations.0.name").String(); got != "bash" {
		t.Errorf("declared tool = %q, want bash unchanged", got)
	}
	// Upstream labels every agent request itself, so only the CLI-only traits are checked.
	if httpReq.ContentLength != int64(len(raw)) {
		t.Errorf("default identity changed: contentLength=%d, want %d", httpReq.ContentLength, len(raw))
	}
	if strings.Contains(httpReq.Header.Get("User-Agent"), "antigravity/cli/") {
		t.Errorf("User-Agent = %q, want hub identity", httpReq.Header.Get("User-Agent"))
	}
}
