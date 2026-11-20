package helps

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

const antigravityCLITestCallPath = "response.candidates.0.content.parts.0.functionCall"

func antigravityCLITestRequest(declarations ...string) *ojson.Doc {
	return ojson.NewDocString(`{"requestType":"agent","request":{"sessionId":"-1","tools":[{"functionDeclarations":[` + strings.Join(declarations, ",") + `]}]}}`)
}

// antigravityCLITestContractRoutes admits all four contracts.
func antigravityCLITestContractRoutes() *AntigravityCLIToolRoutes {
	return newAntigravityCLIToolRoutes(antigravityCLITestRequest(
		`{"name":"read","parameters":{"type":"object","required":["path"]}}`,
		`{"name":"grep","parameters":{"type":"object","required":["path","pattern"]}}`,
		`{"name":"glob","parameters":{"type":"object","required":["path","limit","gitignore","hidden"]}}`,
		`{"name":"write","description":"Write a file","parameters":{"type":"object","required":["path","content"]}}`,
	))
}

func antigravityCLITestEvent(functionCall string) []byte {
	return []byte(`{"response":{"candidates":[{"content":{"parts":[{"functionCall":` + functionCall + `}]}}]}}`)
}

func antigravityCLITestDeclaredNames(doc *ojson.Doc) string {
	var names []string
	for _, tool := range doc.Get("request.tools").Array() {
		for _, declaration := range tool.Get("functionDeclarations").Array() {
			names = append(names, declaration.Get("name").String())
		}
	}
	return strings.Join(names, ",")
}

func TestAntigravityCLIToolNamesKeepOccupiedNames(t *testing.T) {
	const object = `,"parameters":{"type":"object"}}`
	for _, tc := range []struct {
		name         string
		declarations []string
		want         string
	}{
		{"alias already declared by the client", []string{`{"name":"bash"` + object, `{"name":"run_command"` + object}, "bash,run_command"},
		{"edit wins over apply_patch", []string{`{"name":"edit"` + object, `{"name":"apply_patch"` + object}, "edit_file,apply_patch"},
		{"apply_patch alone takes edit_file", []string{`{"name":"apply_patch"` + object}, "edit_file"},
		{"CLI tool name already declared by the client", []string{`{"name":"glob"` + object, `{"name":"find_by_name"` + object}, "glob,find_by_name"},
		{"device-only write keeps the client route", []string{`{"name":"write","description":"Write to xd://<tool>"` + object}, "write"},
		{"filesystem write gains write_file", []string{`{"name":"write","description":"Write a file"` + object}, "write_file,write"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := antigravityCLITestRequest(tc.declarations...)
			ApplyAntigravityCLIIdentityDoc(doc, "gemini-3-flash")
			if got := antigravityCLITestDeclaredNames(doc); got != tc.want {
				t.Fatalf("declared tools = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestAntigravityCLIToolNamesSeeSnakeCaseSpellings(t *testing.T) {
	// Every tool field uses the snake-case spelling Gemini clients may send.
	doc := ojson.NewDocString(`{"requestType":"agent","request":{"sessionId":"-1",
		"tools":[{"functionDeclarations":[{"name":"bash"}]},{"function_declarations":[{"name":"run_command"},{"name":"ask"}]}],
		"tool_config":{"function_calling_config":{"mode":"ANY","allowed_function_names":["ask"]}},
		"contents":[
			{"role":"model","parts":[{"function_call":{"name":"ask","args":{}}}]},
			{"role":"user","parts":[{"function_response":{"name":"ask","response":{}}}]}]}}`)
	ApplyAntigravityCLIIdentityDoc(doc, "gemini-3-flash")
	// run_command is the client's own tool, so bash keeps its name; ask is free.
	if got, want := antigravityCLITestDeclaredNames(doc), "bash,run_command,ask_question"; got != want {
		t.Fatalf("declared tools = %s, want %s", got, want)
	}
	for path, want := range map[string]string{
		antigravityCLIAllowedNamesPath:                     `["ask_question"]`,
		"request.contents.0.parts.0.functionCall.name":     `"ask_question"`,
		"request.contents.1.parts.0.functionResponse.name": `"ask_question"`,
	} {
		if got := doc.Get(path).Raw; got != want {
			t.Errorf("%s = %s, want %s", path, got, want)
		}
	}
	if strings.Contains(doc.String(), `"ask"`) {
		t.Fatalf("the client's tool name is still sent somewhere: %s", doc.String())
	}
}

func TestAntigravityCLIContractArgumentsRoundTrip(t *testing.T) {
	routes := antigravityCLITestContractRoutes()
	for _, tc := range []struct {
		name, host, native, nativeArgs, hostArgs string
	}{
		{"whole file", "read", "read_file", `{"Path":"a/b.go"}`, `{"path":"a/b.go:raw"}`},
		{"open-ended range", "read", "read_file", `{"Path":"b.go","StartLine":7}`, `{"path":"b.go:raw:7-"}`},
		{"largest line number", "read", "read_file", `{"Path":"b.go","StartLine":9007199254740991}`, `{"path":"b.go:raw:9007199254740991-"}`},
		{"literal query is escaped", "grep", "grep_search", `{"SearchPath":".","Query":"a.b(c)","MatchPerLine":true,"IsRegex":false,"CaseInsensitive":true}`, `{"path":".","pattern":"a\\.b\\(c\\)","case":false}`},
		{"lowercase name matches any case", "glob", "find_by_name", `{"SearchDirectory":"src","Pattern":"*.go"}`, `{"path":"src/**/*.[gG][oO]","limit":50,"gitignore":true,"hidden":false}`},
		{"workspace root", "glob", "find_by_name", `{"SearchDirectory":".","Pattern":"README"}`, `{"path":"**/README","limit":50,"gitignore":true,"hidden":false}`},
		{"write", "write", "write_file", `{"Path":"out.txt","Content":"x\ny","Overwrite":true}`, `{"path":"out.txt","content":"x\ny"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := antigravityCLITestEvent(`{"name":"` + tc.native + `","args":` + tc.nativeArgs + `}`)
			call := ojson.ParseBytes(routes.NewResponseRestorer().Restore(event)).Get(antigravityCLITestCallPath)
			if call.Get("name").String() != tc.host || call.Get("args").Raw != tc.hostArgs {
				t.Fatalf("restored call = %s, want %s %s", call.Raw, tc.host, tc.hostArgs)
			}
			name, args := routes.replay(tc.host, ojson.Parse(tc.hostArgs))
			if name != tc.native || args != tc.nativeArgs {
				t.Fatalf("replayed call = %s %s, want %s %s", name, args, tc.native, tc.nativeArgs)
			}
		})
	}
}

// A CLI call outside its contract must stay under the CLI name: the client has no
// such tool, so nothing runs. Renaming it would hand the client's tool arguments
// the model never issued through the contract.
func TestAntigravityCLIContractCallsOutsideTheSchemaStayUndispatchable(t *testing.T) {
	routes := antigravityCLITestContractRoutes()
	for _, tc := range []struct{ name, native, args string }{
		{"client argument names on write_file", "write_file", `{"path":"notes.txt","content":"example"}`},
		{"write without overwrite", "write_file", `{"Path":"a","Content":"x","Overwrite":false}`},
		{"unknown argument", "write_file", `{"Path":"a","Content":"x","Overwrite":true,"Append":true}`},
		{"repeated destination", "write_file", `{"Path":"one","Path":"two","Content":"x","Overwrite":true}`},
		{"parent traversal", "read_file", `{"Path":"../etc/passwd"}`},
		{"absolute read", "read_file", `{"Path":"/etc/passwd"}`},
		{"range ends before it starts", "read_file", `{"Path":"a","StartLine":9,"EndLine":2}`},
		{"line number beyond exact integers", "read_file", `{"Path":"a","StartLine":9007199254740993}`},
		{"regex query", "grep_search", `{"SearchPath":".","Query":"a\\d","MatchPerLine":true,"IsRegex":false}`},
		{"regex mode", "grep_search", `{"SearchPath":".","Query":"a","MatchPerLine":true,"IsRegex":true}`},
		{"query blank to ECMAScript trim", "grep_search", `{"SearchPath":".","Query":"\ufeff","MatchPerLine":true,"IsRegex":false}`},
		{"path separator in name pattern", "find_by_name", `{"SearchDirectory":".","Pattern":"a/b"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := antigravityCLITestEvent(`{"name":"` + tc.native + `","args":` + tc.args + `}`)
			call := ojson.ParseBytes(routes.NewResponseRestorer().Restore(event)).Get(antigravityCLITestCallPath)
			if call.Get("name").String() != tc.native || call.Get("args").Raw != tc.args {
				t.Fatalf("out-of-contract call became %s", call.Raw)
			}
		})
	}
}

func TestAntigravityCLIReplayKeepsCallsTheCLIToolCannotExpress(t *testing.T) {
	routes := antigravityCLITestContractRoutes()
	for _, tc := range []struct{ name, host, args string }{
		{"URL read", "read", `{"path":"https://example.com"}`},
		{"read without raw selector", "read", `{"path":"a.go:10-20"}`},
		{"read range beyond exact integers", "read", `{"path":"a.go:raw:9007199254740993-"}`},
		{"regex grep", "grep", `{"path":".","pattern":"a.*b"}`},
		{"glob without recursion", "glob", `{"path":"src/*.go","limit":50,"gitignore":true,"hidden":false}`},
		{"glob including hidden files", "glob", `{"path":"**/README","limit":50,"gitignore":true,"hidden":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if name, args := routes.replay(tc.host, ojson.Parse(tc.args)); name != tc.host || args != "" {
				t.Fatalf("replayed call = %s %s, want the client's own tool", name, args)
			}
		})
	}
}

// A contract call that does not arrive whole in one event is never converted and
// never renamed: every event of it reaches the client under the CLI name, which
// the client cannot run.
func TestAntigravityCLIContractCallsNotWholeInOneEventStayUndispatchable(t *testing.T) {
	for _, tc := range []struct{ name, call string }{
		{"fragmented arguments", `{"name":"write_file","partialArgs":[{"jsonPath":"$.Path","stringValue":"a"}],"willContinue":true}`},
		{"name without arguments", `{"id":"A","name":"read_file"}`},
		{"empty arguments", `{"id":"A","name":"read_file","args":{}}`},
		{"nameless continuation", `{"id":"A","args":{"Path":"one"}}`},
		{"complete arguments marked as continuing", `{"name":"read_file","args":{"Path":"one"},"willContinue":true}`},
		{"two argument members", `{"name":"write_file","args":{"Path":"first","Content":"x","Overwrite":true},"args":{"path":"second","content":"y"}}`},
		{"two names", `{"name":"read_file","name":"write_file","args":{"Path":"one"}}`},
		{"fragments under the other spelling", `{"name":"write_file","args":{"Path":"a","Content":"x","Overwrite":true},"partialArgs":[],"partial_args":[{"jsonPath":"$.Content","stringValue":"y"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := antigravityCLITestEvent(tc.call)
			restored := antigravityCLITestContractRoutes().NewResponseRestorer().Restore(event)
			if string(restored) != string(event) {
				t.Fatalf("event was rewritten: %s", restored)
			}
		})
	}
}

func TestAntigravityCLIAliasCallKeepsItsFragments(t *testing.T) {
	routes := newAntigravityCLIToolRoutes(antigravityCLITestRequest(`{"name":"bash","parameters":{"type":"object"}}`))
	const fragments = `"partialArgs":[{"jsonPath":"$.command","stringValue":"ls"}],"willContinue":true}`
	call := ojson.ParseBytes(routes.NewResponseRestorer().Restore(antigravityCLITestEvent(`{"name":"run_command",` + fragments))).Get(antigravityCLITestCallPath)
	if got, want := call.Raw, `{"name":"bash",`+fragments; got != want {
		t.Fatalf("restored call = %s, want %s", got, want)
	}
}

func TestAntigravityCLIPolicySpellingsAreMergedNotDropped(t *testing.T) {
	// The executor already set a camel-case mode; the client's allow-list and its
	// request for fragmented arguments arrive in snake case, at either depth.
	for name, clientPolicy := range map[string]string{
		"snake case throughout":        `"tool_config":{"function_calling_config":{"mode":"ANY","allowed_function_names":["lookup"],"stream_function_call_arguments":true}}`,
		"camel case inside snake case": `"tool_config":{"functionCallingConfig":{"mode":"ANY","allowed_function_names":["lookup"],"streamFunctionCallArguments":true}}`,
	} {
		t.Run(name, func(t *testing.T) {
			doc := ojson.NewDocString(`{"requestType":"agent","request":{"sessionId":"-1",
				"toolConfig":{"functionCallingConfig":{"mode":"VALIDATED"}},` + clientPolicy + `,
				"tools":[{"functionDeclarations":[{"name":"bash"},{"name":"lookup"}]}]}}`)
			ApplyAntigravityCLIIdentityDoc(doc, "claude-sonnet-4-6")
			if got, want := doc.Get("request.toolConfig").Raw, `{"functionCallingConfig":{"mode":"VALIDATED","allowedFunctionNames":["lookup"]}}`; got != want {
				t.Fatalf("toolConfig = %s, want %s", got, want)
			}
			if doc.Get("request.tool_config").Exists() {
				t.Fatalf("snake-case policy survived next to the merged one")
			}
		})
	}
}

func TestAntigravityCLIPartNamingTwoCallsIsLeftAlone(t *testing.T) {
	event := []byte(`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"write_file","args":{"Path":"one","Content":"approved","Overwrite":true}},"functionCall":{"name":"preview","args":{}}}]}}]}}`)
	if restored := antigravityCLITestContractRoutes().NewResponseRestorer().Restore(event); string(restored) != string(event) {
		t.Fatalf("ambiguous part was rewritten: %s", restored)
	}
}

// Every object on the way to a call is checked: whichever duplicate a parser
// picks, the client must not receive a call the other one did not contain.
func TestAntigravityCLIAmbiguousResponsePathIsLeftAlone(t *testing.T) {
	const write = `{"parts":[{"functionCall":{"name":"write_file","args":{"Path":"one","Content":"approved","Overwrite":true}}}]}`
	const preview = `{"parts":[{"functionCall":{"name":"preview","args":{}}}]}`
	for name, event := range map[string]string{
		"two parts members":      `{"response":{"candidates":[{"content":{"parts":` + write[9:len(write)-1] + `,"parts":` + preview[9:len(preview)-1] + `}}]}}`,
		"two content members":    `{"response":{"candidates":[{"content":` + write + `,"content":` + preview + `}]}}`,
		"two candidates members": `{"response":{"candidates":[{"content":` + write + `}],"candidates":[{"content":` + preview + `}]}}`,
		"two response members":   `{"response":{"candidates":[{"content":` + write + `}]},"response":{"candidates":[{"content":` + preview + `}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if restored := antigravityCLITestContractRoutes().NewResponseRestorer().Restore([]byte(event)); string(restored) != event {
				t.Fatalf("ambiguous response was rewritten: %s", restored)
			}
		})
	}
}

func TestAntigravityCLIConflictingSpellingsKeepTheCamelCaseValue(t *testing.T) {
	doc := ojson.NewDocString(`{"requestType":"agent","request":{"sessionId":"-1",
		"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["lookup"],"allowed_function_names":["bash"]}},
		"tools":[{"functionDeclarations":[{"name":"bash"},{"name":"lookup"}]}],
		"contents":[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"fields":{"x":1}}},"function_call":{"name":"other","args":{"fields":{"a.b":2}}}}]}]}}`)
	ApplyAntigravityCLIIdentityDoc(doc, "gemini-3-flash")
	if got, want := doc.Get(antigravityCLIAllowedNamesPath).Raw, `["lookup"]`; got != want {
		t.Fatalf("allowed names = %s, want %s: joining the lists grants a tool", got, want)
	}
	if got, want := doc.Get("request.contents.0.parts.0.functionCall").Raw, `{"name":"lookup","args":{"fields":{"x":1,"a.b":2}}}`; got != want {
		t.Fatalf("merged call = %s, want %s", got, want)
	}
}

// antigravityCLITestReconcile applies the CLI identity, lets edit play a payload
// rule on the upstream body, and returns the tools declared in the local view
// together with the client names that response calls are still mapped to.
func antigravityCLITestReconcile(t *testing.T, edit func(doc *ojson.Doc)) (local string, mapped map[string]string) {
	t.Helper()
	doc := antigravityCLITestRequest(
		`{"name":"bash","parameters":{"type":"object","properties":{"limit":{"type":"integer","enum":[9007199254740992]}}}}`,
		`{"name":"write","description":"Write a file","parameters":{"type":"object","required":["path","content"]}}`,
	)
	tools := ApplyAntigravityCLIIdentityDoc(doc, "gemini-3-flash")
	edit(doc)
	tools.RestoreClientView(doc)
	mapped = map[string]string{}
	restorer := tools.Routes().NewResponseRestorer()
	for _, call := range []string{`{"name":"run_command","args":{"command":"pwd"}}`, `{"name":"write_file","args":{"Path":"a","Content":"x","Overwrite":true}}`} {
		name := ojson.Parse(call).Get("name").String()
		mapped[name] = ojson.ParseBytes(restorer.Restore(antigravityCLITestEvent(call))).Get(antigravityCLITestCallPath + ".name").String()
	}
	return antigravityCLITestDeclaredNames(doc), mapped
}

func TestAntigravityCLIRoutesSurviveOnlyUnambiguousGeneratedDeclarations(t *testing.T) {
	const wireTools = "request.tools"
	for _, tc := range []struct {
		name      string
		edit      func(doc *ojson.Doc)
		wantLocal string
		wantBash  string
		wantWrite string
	}{
		{"untouched", func(*ojson.Doc) {}, "bash,write", "bash", "write"},
		{"regrouped into one entry", func(doc *ojson.Doc) {
			var declarations []string
			for _, tool := range doc.Get(wireTools).Array() {
				declarations = append(declarations, tool.Get("functionDeclarations.0").Raw)
			}
			_ = doc.SetRaw(wireTools, `[{"functionDeclarations":[`+strings.Join(declarations, ",")+`]}]`)
		}, "bash,write", "bash", "write"},
		{"second declaration under the alias", func(doc *ojson.Doc) {
			_ = doc.SetRaw(wireTools+".-1", `{"functionDeclarations":[{"name":"run_command","description":"Record only"}]}`)
		}, "run_command,write,run_command", "run_command", "write"},
		{"second declaration under the alias, snake-case group", func(doc *ojson.Doc) {
			_ = doc.SetRaw(wireTools+".-1", `{"function_declarations":[{"name":"run_command","description":"Record only"}]}`)
		}, "run_command,write", "run_command", "write"},
		{"client name declared again", func(doc *ojson.Doc) {
			_ = doc.SetRaw(wireTools+".-1", `{"functionDeclarations":[{"name":"bash"}]}`)
		}, "run_command,write,bash", "run_command", "write"},
		{"alias schema number changed by one", func(doc *ojson.Doc) {
			_ = doc.SetRaw(wireTools+".0.functionDeclarations.0.parameters.properties.limit.enum.0", "9007199254740993")
		}, "run_command,write", "run_command", "write"},
		{"client write declaration removed upstream", func(doc *ojson.Doc) {
			_ = doc.Delete(wireTools + ".2")
		}, "bash,write", "bash", "write"},
		{"generated write_file redefined", func(doc *ojson.Doc) {
			_ = doc.Set(wireTools+".1.functionDeclarations.0.description", "Preview only")
		}, "bash,write_file,write", "bash", "write_file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local, mapped := antigravityCLITestReconcile(t, tc.edit)
			if local != tc.wantLocal || mapped["run_command"] != tc.wantBash || mapped["write_file"] != tc.wantWrite {
				t.Fatalf("local tools = %s, run_command -> %s, write_file -> %s; want %s, %s, %s",
					local, mapped["run_command"], mapped["write_file"], tc.wantLocal, tc.wantBash, tc.wantWrite)
			}
		})
	}
}

// A rule may regroup generated declarations under the snake-case spelling; the
// local view gets the client's declaration back there, and siblings stay.
func TestAntigravityCLILocalViewRestoresSnakeCaseGroups(t *testing.T) {
	doc := antigravityCLITestRequest(`{"name":"bash","parameters":{"type":"object"}}`)
	tools := ApplyAntigravityCLIIdentityDoc(doc, "gemini-3-flash")
	generated := doc.Get("request.tools.0.functionDeclarations.0").Raw
	_ = doc.SetRaw("request.tools.0", `{"function_declarations":[`+generated+`,{"name":"other"}]}`)
	tools.RestoreClientView(doc)
	if got, want := doc.Get("request.tools.0").Raw, `{"function_declarations":[{"name":"bash","parameters":{"type":"object"}},{"name":"other"}]}`; got != want {
		t.Fatalf("local tools entry = %s, want %s", got, want)
	}
}

func TestAntigravityCLIResultsFollowTheirOwnCall(t *testing.T) {
	// Call 1 is sent as read_file, call 2 stays read. Their results arrive out of
	// order, and the result of call 1 carries no id.
	doc := ojson.NewDocString(`{"requestType":"agent","request":{"sessionId":"-1",
		"tools":[{"functionDeclarations":[{"name":"read","parameters":{"type":"object","required":["path"]}}]}],
		"contents":[
			{"role":"model","parts":[
				{"functionCall":{"id":"1","name":"read","args":{"path":"a.go:raw"}}},
				{"functionCall":{"id":"2","name":"read","args":{"path":"https://example.com"}}}]},
			{"role":"user","parts":[
				{"functionResponse":{"id":"2","name":"read","response":{}}},
				{"functionResponse":{"name":"read","response":{}}}]}]}}`)
	ApplyAntigravityCLIIdentityDoc(doc, "gemini-3-flash")
	results := doc.Get("request.contents.1.parts")
	if got, want := results.Get("0.functionResponse.name").String()+","+results.Get("1.functionResponse.name").String(), "read,read_file"; got != want {
		t.Fatalf("result names = %s, want %s", got, want)
	}
}

func TestAntigravityCLICreditOptInSurvives(t *testing.T) {
	doc := ojson.NewDocString(`{"requestType":"agent","enabledCreditTypes":["GOOGLE_ONE_AI"],"request":{"sessionId":"-1"}}`)
	ApplyAntigravityCLIIdentityDoc(doc, "gemini-3-flash")
	if got := doc.Get("enabledCreditTypes.0").String(); got != "GOOGLE_ONE_AI" {
		t.Fatalf("enabledCreditTypes = %s, want the executor's credit opt-in kept", doc.Get("enabledCreditTypes").Raw)
	}
}

func TestApplyAntigravityCLIIdentityDocSkipsNonAgentRequests(t *testing.T) {
	const payload = `{"requestType":"image_gen","requestId":"image_gen/1/x/12","request":{"tools":[{"functionDeclarations":[{"name":"bash"}]}]}}`
	doc := ojson.NewDocString(payload)
	if tools := ApplyAntigravityCLIIdentityDoc(doc, "gemini-3-image"); tools != nil || doc.String() != payload {
		t.Fatalf("image request changed: doc=%s", doc.String())
	}
}

func TestAntigravityCLIConversationIDsAreStablePerSession(t *testing.T) {
	ids := func(session string) (string, string) {
		doc := ojson.NewDocString(`{"requestType":"agent","request":{"sessionId":"` + session + `"}}`)
		ApplyAntigravityCLIIdentityDoc(doc, "gemini-3-flash")
		return doc.Get("request.labels.cascade_id").String(), doc.Get("request.labels.trajectory_id").String()
	}
	cascadeA, trajectoryA := ids("-111")
	cascadeAgain, trajectoryAgain := ids("-111")
	cascadeB, _ := ids("-222")
	if cascadeA != cascadeAgain || trajectoryA != trajectoryAgain {
		t.Fatalf("same session produced different ids: %s/%s vs %s/%s", cascadeA, trajectoryA, cascadeAgain, trajectoryAgain)
	}
	if cascadeA == cascadeB {
		t.Fatalf("different sessions share cascade id %s", cascadeA)
	}
}
