package helps

import (
	"bytes"
	"context"
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

const (
	antigravityCLIFunctionCallingPath = "request.toolConfig.functionCallingConfig"
	antigravityCLIAllowedNamesPath    = antigravityCLIFunctionCallingPath + ".allowedFunctionNames"
)

// antigravityCLIContractRoute is a contract admitted for one request, together
// with the client's own declaration and its intent field when it declares one.
type antigravityCLIContractRoute struct {
	contract        *antigravityCLIToolContract
	hostDeclaration string
	intentSchema    string
	intentRequired  bool
}

// antigravityCLIAliasDeclaration is a client declaration sent under a CLI name.
type antigravityCLIAliasDeclaration struct{ generated, original string }

// AntigravityCLIToolRoutes decides, for one request, which client tools travel
// under a CLI name. Every name the request already uses keeps its identity, so an
// occupied CLI name leaves the client tool unchanged rather than merging two tools.
type AntigravityCLIToolRoutes struct {
	outgoing  map[string]string
	contracts map[string]*antigravityCLIContractRoute
	incoming  map[string]string
	// aliased holds, by client name, the declarations sent under an alias.
	aliased map[string]antigravityCLIAliasDeclaration
}

// antigravityCLIEdit is one rewritten history or allow-list location of a request:
// the JSON written there and the client's JSON it replaced.
type antigravityCLIEdit struct{ path, generated, original string }

// AntigravityCLIRequestTools is the tool rewriting applied to one request.
type AntigravityCLIRequestTools struct {
	routes *AntigravityCLIToolRoutes
	edits  []antigravityCLIEdit
}

type antigravityCLIToolRoutesKey struct{}

// WithAntigravityCLIToolRoutes attaches the routes of a built request to its context.
func WithAntigravityCLIToolRoutes(ctx context.Context, routes *AntigravityCLIToolRoutes) context.Context {
	if routes == nil {
		return ctx
	}
	return context.WithValue(ctx, antigravityCLIToolRoutesKey{}, routes)
}

// AntigravityCLIToolRoutesFrom returns the routes attached to a request context,
// or nil when the request was built without the CLI identity.
func AntigravityCLIToolRoutesFrom(ctx context.Context) *AntigravityCLIToolRoutes {
	routes, _ := ctx.Value(antigravityCLIToolRoutesKey{}).(*AntigravityCLIToolRoutes)
	return routes
}

// normalizeAntigravityCLIToolSpellingsDoc moves the snake-case spellings Gemini
// clients may use for tool policy, declarations and history onto the camel-case
// fields the CLI sends, so route discovery and rewriting see every tool name.
// It also drops the request for fragmented function-call arguments, which the CLI
// never sends: contract arguments can only be converted when they arrive whole.
func normalizeAntigravityCLIToolSpellingsDoc(doc *ojson.Doc) {
	// Each move is applied at once: later paths run through earlier renames.
	// Where both spellings exist the camel-case value wins; objects are merged
	// member by member, and only declaration lists (join) are concatenated: a
	// joined allow-list would grant tools neither spelling granted alone.
	move := func(base, snake, camel string, join bool) {
		legacy := doc.Get(base + "." + snake)
		if !legacy.Exists() {
			return
		}
		current := doc.Get(base + "." + camel)
		var replacement string
		switch {
		case !current.Exists():
			replacement = strings.Clone(legacy.Raw)
		case join && current.IsArray() && legacy.IsArray():
			var items []string
			for _, list := range [...]ojson.Result{current, legacy} {
				for _, item := range list.Array() {
					items = append(items, item.Raw)
				}
			}
			replacement = "[" + strings.Join(items, ",") + "]"
		default:
			replacement = antigravityCLIMergeObjects(current, legacy)
		}
		_ = doc.Delete(base + "." + snake)
		_ = doc.SetRaw(base+"."+camel, replacement)
	}
	move("request", "tool_config", "toolConfig", false)
	move("request.toolConfig", "function_calling_config", "functionCallingConfig", false)
	move(antigravityCLIFunctionCallingPath, "allowed_function_names", "allowedFunctionNames", false)
	_ = doc.Delete(antigravityCLIFunctionCallingPath + ".streamFunctionCallArguments")
	_ = doc.Delete(antigravityCLIFunctionCallingPath + ".stream_function_call_arguments")
	for i := int(doc.Get("request.tools.#").Int()) - 1; i >= 0; i-- {
		move("request.tools."+strconv.Itoa(i), "function_declarations", "functionDeclarations", true)
	}
	for ci := int(doc.Get("request.contents.#").Int()) - 1; ci >= 0; ci-- {
		parts := "request.contents." + strconv.Itoa(ci) + ".parts"
		for pi := int(doc.Get(parts+".#").Int()) - 1; pi >= 0; pi-- {
			part := parts + "." + strconv.Itoa(pi)
			move(part, "function_call", "functionCall", false)
			move(part, "function_response", "functionResponse", false)
		}
	}
}

// antigravityCLIMergeObjects returns current extended with the members only legacy
// has, at every depth. Any other conflict keeps current's value. Member names are
// copied as written, never interpreted as paths.
func antigravityCLIMergeObjects(current, legacy ojson.Result) string {
	if !current.IsObject() || !legacy.IsObject() {
		return strings.Clone(current.Raw)
	}
	var (
		keys   []string
		values []ojson.Result
		raws   []string
	)
	index := map[string]int{}
	current.ForEach(func(key, value ojson.Result) bool {
		index[key.String()] = len(keys)
		keys = append(keys, key.Raw)
		values = append(values, value)
		raws = append(raws, value.Raw)
		return true
	})
	legacy.ForEach(func(key, value ojson.Result) bool {
		if i, ok := index[key.String()]; ok {
			raws[i] = antigravityCLIMergeObjects(values[i], value)
			return true
		}
		keys = append(keys, key.Raw)
		raws = append(raws, value.Raw)
		return true
	})
	var merged strings.Builder
	merged.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			merged.WriteByte(',')
		}
		merged.WriteString(key)
		merged.WriteByte(':')
		merged.WriteString(raws[i])
	}
	merged.WriteByte('}')
	return merged.String()
}

func newAntigravityCLIToolRoutes(doc *ojson.Doc) *AntigravityCLIToolRoutes {
	routes := &AntigravityCLIToolRoutes{
		outgoing:  map[string]string{},
		contracts: map[string]*antigravityCLIContractRoute{},
		incoming:  map[string]string{},
		aliased:   map[string]antigravityCLIAliasDeclaration{},
	}
	originals := map[string]struct{}{}
	var declarations []ojson.Result
	for _, tool := range doc.Get("request.tools").Array() {
		for _, declaration := range tool.Get("functionDeclarations").Array() {
			if name := declaration.Get("name"); name.Type == ojson.String {
				originals[name.String()] = struct{}{}
				declarations = append(declarations, declaration)
			}
		}
	}
	for _, content := range doc.Get("request.contents").Array() {
		for _, part := range content.Get("parts").Array() {
			if name := part.Get("functionCall.name"); name.Type == ojson.String {
				originals[name.String()] = struct{}{}
			}
		}
	}
	for _, name := range doc.Get(antigravityCLIAllowedNamesPath).Array() {
		if name.Type == ojson.String {
			originals[name.String()] = struct{}{}
		}
	}

	for _, pair := range antigravityCLIToolAliases {
		original, alias := pair[0], pair[1]
		_, used := originals[original]
		_, taken := originals[alias]
		_, hasEdit := originals["edit"]
		if !used || taken || (original == "apply_patch" && hasEdit) {
			continue
		}
		routes.outgoing[original] = alias
		routes.incoming[alias] = original
	}
	for _, declaration := range declarations {
		route := admitAntigravityCLIContract(declaration, originals)
		if route == nil {
			continue
		}
		route.hostDeclaration = strings.Clone(declaration.Raw)
		routes.contracts[route.contract.hostName] = route
		routes.incoming[route.contract.nativeName] = route.contract.hostName
	}
	return routes
}

// admitAntigravityCLIContract returns the contract route for a declared client tool,
// or nil when the tool's own schema demands arguments the CLI tool cannot supply.
func admitAntigravityCLIContract(declaration ojson.Result, originals map[string]struct{}) *antigravityCLIContractRoute {
	name := declaration.Get("name").String()
	var contract *antigravityCLIToolContract
	for _, candidate := range antigravityCLIToolContracts {
		if candidate.hostName == name {
			contract = candidate
			break
		}
	}
	if contract == nil {
		return nil
	}
	if _, taken := originals[contract.nativeName]; taken {
		return nil
	}
	// A device-only write is not a filesystem capability, and a pruned description
	// does not establish that filesystem writes are available.
	if description := declaration.Get("description").String(); name == "write" &&
		(strings.TrimSpace(description) == "" || strings.Contains(description, "xd://<tool>")) {
		return nil
	}
	parameters := declaration.Get("parameters")
	if !parameters.IsObject() {
		return nil
	}
	intent := parameters.Get("properties.i")
	route := &antigravityCLIContractRoute{contract: contract}
	for _, required := range parameters.Get("required").Array() {
		key := required.String()
		if key == "i" {
			if !intent.Exists() {
				return nil
			}
			route.intentRequired = true
			continue
		}
		supplied := false
		for _, hostKey := range contract.hostRequired {
			supplied = supplied || hostKey == key
		}
		if !supplied {
			return nil
		}
	}
	if intent.Exists() {
		if !intent.IsObject() || !strings.EqualFold(intent.Get("type").String(), "string") {
			return nil
		}
		route.intentSchema = strings.Clone(intent.Raw)
	}
	return route
}

// applyRequestDoc rewrites declarations, replayed calls and results, and an explicit
// allowed-name list to their CLI form, and records what each location held.
func (r *AntigravityCLIToolRoutes) applyRequestDoc(doc *ojson.Doc) *AntigravityCLIRequestTools {
	// Collect every edit before the first write: the paths are index-based.
	var edits []antigravityCLIEdit
	var tools string
	record := func(path, generated, original string) {
		edits = append(edits, antigravityCLIEdit{path: path, generated: generated, original: strings.Clone(original)})
	}

	if declared := doc.Get("request.tools"); declared.IsArray() {
		// Every declaration gets its own entry, as the CLI sends them.
		var entries []string
		changed := false
		for _, tool := range declared.Array() {
			declarations := tool.Get("functionDeclarations")
			if !declarations.IsArray() {
				entries = append(entries, strings.Clone(tool.Raw))
				continue
			}
			list := declarations.Array()
			changed = changed || len(list) != 1
			for _, declaration := range list {
				name := declaration.Get("name").String()
				if route := r.contracts[name]; route != nil {
					// The CLI tool is declared alongside the client's own, which
					// keeps the client's full argument surface available.
					entries = append(entries, `{"functionDeclarations":[`+route.declaration()+`]}`)
					changed = true
				}
				raw := strings.Clone(declaration.Raw)
				if alias, ok := r.outgoing[name]; ok {
					aliased := ojson.NewDocString(raw)
					_ = aliased.Set("name", alias)
					r.aliased[name] = antigravityCLIAliasDeclaration{generated: aliased.String(), original: raw}
					raw = aliased.String()
					changed = true
				}
				entries = append(entries, `{"functionDeclarations":[`+raw+`]}`)
			}
		}
		if changed && len(entries) > 0 {
			tools = "[" + strings.Join(entries, ",") + "]"
		}
	}

	// A result takes the name its call was sent under: the call with the same id
	// when the client supplies one, otherwise the oldest unanswered call of that name.
	type sentCall struct {
		sent     string
		answered bool
	}
	callsByID := map[string]*sentCall{}
	callsByName := map[string][]*sentCall{}
	for ci, content := range doc.Get("request.contents").Array() {
		for pi, part := range content.Get("parts").Array() {
			partPath := "request.contents." + strconv.Itoa(ci) + ".parts." + strconv.Itoa(pi)
			if call := part.Get("functionCall"); call.Exists() {
				name := call.Get("name").String()
				sentName, sentArgs := r.replay(name, call.Get("args"))
				sent := &sentCall{sent: sentName}
				if id := call.Get("id").String(); id != "" {
					callsByID[id] = sent
				}
				callsByName[name] = append(callsByName[name], sent)
				if sentName != name {
					rewritten := ojson.NewDocString(strings.Clone(call.Raw))
					_ = rewritten.Set("name", sentName)
					if sentArgs != "" {
						_ = rewritten.SetRaw("args", sentArgs)
					}
					record(partPath+".functionCall", rewritten.String(), call.Raw)
				}
			}
			if result := part.Get("functionResponse.name"); result.Type == ojson.String {
				name := result.String()
				call := callsByID[part.Get("functionResponse.id").String()]
				if call == nil || call.answered {
					call = nil
					for _, candidate := range callsByName[name] {
						if !candidate.answered {
							call = candidate
							break
						}
					}
				}
				sentName, _ := r.replay(name, ojson.Result{})
				if call != nil {
					call.answered = true
					sentName = call.sent
				}
				if sentName != name {
					record(partPath+".functionResponse.name", antigravityCLIJSONString(sentName), result.Raw)
				}
			}
		}
	}

	if allowed := doc.Get(antigravityCLIAllowedNamesPath); allowed.IsArray() {
		var names []string
		changed := false
		for _, name := range allowed.Array() {
			if route := r.contracts[name.String()]; route != nil {
				names = append(names, antigravityCLIJSONString(route.contract.nativeName))
				changed = true
			} else if alias, ok := r.outgoing[name.String()]; ok {
				names = append(names, antigravityCLIJSONString(alias))
				changed = true
				continue
			}
			names = append(names, strings.Clone(name.Raw))
		}
		if changed {
			record(antigravityCLIAllowedNamesPath, "["+strings.Join(names, ",")+"]", allowed.Raw)
		}
	}

	if tools != "" {
		_ = doc.SetRaw("request.tools", tools)
	}
	for _, edit := range edits {
		_ = doc.SetRaw(edit.path, edit.generated)
	}
	return &AntigravityCLIRequestTools{routes: r, edits: edits}
}

// RestoreClientView runs after the user payload rules and the serialization of the
// upstream body. It puts the client's tool names and arguments back into doc for
// local consumers (translators, replay cache) and reports whether doc changed.
//
// A payload rule may have replaced tools or history, so every route is checked
// against all finalized declarations, however a rule grouped them. A route
// survives only while exactly one declaration carries its CLI name, that
// declaration is still the generated one, and an alias's client name has not
// reappeared. A surviving route gets the client's declaration back in the local
// view; any other declaration stays as the rule wrote it and its route stops
// applying to the response. History and allow-list locations are put back only
// while they hold exactly what was generated.
func (t *AntigravityCLIRequestTools) RestoreClientView(doc *ojson.Doc) bool {
	if t == nil {
		return false
	}
	routes := t.routes
	count := map[string]int{}
	finalized := map[string]string{}
	tools := doc.Get("request.tools")
	for _, tool := range tools.Array() {
		// A rule may add declarations under either spelling; both count.
		for _, group := range [...]string{"functionDeclarations", "function_declarations"} {
			for _, declaration := range tool.Get(group).Array() {
				name := declaration.Get("name").String()
				count[name]++
				finalized[name] = declaration.Raw
			}
		}
	}
	for wire, host := range routes.incoming {
		generated := routes.aliased[host].generated
		route := routes.contracts[host]
		if route != nil {
			generated = route.declaration()
		}
		survives := generated != "" && count[wire] == 1 && antigravityCLISameJSON(finalized[wire], generated)
		if route == nil && count[host] > 0 {
			survives = false
		}
		if !survives {
			delete(routes.incoming, wire)
			delete(routes.contracts, host)
			delete(routes.outgoing, host)
		}
	}

	var entries []string
	toolsChanged := false
	for _, tool := range tools.Array() {
		var entry *ojson.Doc
		for _, group := range [...]string{"functionDeclarations", "function_declarations"} {
			var kept []string
			groupChanged := false
			for _, declaration := range tool.Get(group).Array() {
				host, routed := routes.incoming[declaration.Get("name").String()]
				switch route := routes.contracts[host]; {
				case !routed:
					kept = append(kept, strings.Clone(declaration.Raw))
					continue
				case route == nil:
					kept = append(kept, routes.aliased[host].original)
				case count[host] == 0:
					// A rule removed the client's own declaration from the upstream
					// body; the client still has the tool.
					kept = append(kept, route.hostDeclaration)
				}
				groupChanged = true
			}
			if !groupChanged {
				continue
			}
			if entry == nil {
				entry = ojson.NewDocString(strings.Clone(tool.Raw))
			}
			if len(kept) > 0 {
				_ = entry.SetRaw(group, "["+strings.Join(kept, ",")+"]")
			} else {
				_ = entry.Delete(group)
			}
		}
		switch {
		case entry == nil:
			entries = append(entries, strings.Clone(tool.Raw))
		default:
			toolsChanged = true
			// An entry is dropped only when nothing is left in it.
			if rewritten := entry.String(); len(ojson.Parse(rewritten).Map()) > 0 {
				entries = append(entries, rewritten)
			}
		}
	}

	changed := false
	for i := len(t.edits) - 1; i >= 0; i-- {
		edit := t.edits[i]
		if doc.Get(edit.path).Raw != edit.generated {
			continue
		}
		if errSet := doc.SetRaw(edit.path, edit.original); errSet == nil {
			changed = true
		}
	}
	if toolsChanged {
		if errSet := doc.SetRaw("request.tools", "["+strings.Join(entries, ",")+"]"); errSet == nil {
			changed = true
		}
	}
	return changed
}

// Routes returns the routes that apply to the response, or nil when none do.
func (t *AntigravityCLIRequestTools) Routes() *AntigravityCLIToolRoutes {
	if t == nil || len(t.routes.incoming) == 0 {
		return nil
	}
	return t.routes
}

// antigravityCLISameJSON reports whether two JSON texts are identical apart from
// whitespace between tokens. Comparing text keeps every number exact; a rule that
// reorders members counts as a replacement, which only ends a route.
func antigravityCLISameJSON(left, right string) bool {
	return left == right || antigravityCLICompactJSON(left) == antigravityCLICompactJSON(right)
}

func antigravityCLICompactJSON(text string) string {
	var compact strings.Builder
	inString, escaped := false, false
	for i := range len(text) {
		char := text[i]
		switch {
		case inString:
			inString = escaped || char != '"'
			escaped = !escaped && char == '\\'
		case char == ' ' || char == '\t' || char == '\n' || char == '\r':
			continue
		case char == '"':
			inString = true
		}
		compact.WriteByte(char)
	}
	return compact.String()
}

// replay returns the name, and for a contract tool the converted arguments, under
// which a client call is sent upstream. A call the CLI tool cannot express keeps
// the client's name and arguments; its own declaration is still present.
//
// A client read is replayed as read_file whenever its path carries the ":raw"
// selector that decoding produces. The proxy keeps no per-call state, so it cannot
// tell such a call from a client-initiated raw read of the same path. Both name
// the same file unless one literally called "<path>:raw" exists, and in that case
// the decoded call itself already read that file: the proxy cannot see the
// client's filesystem to rule it out.
func (r *AntigravityCLIToolRoutes) replay(name string, args ojson.Result) (sentName, sentArgs string) {
	if route := r.contracts[name]; route != nil {
		if native, ok := route.convert(args, route.contract.encode, route.contract.decode); ok {
			return route.contract.nativeName, native
		}
		return name, ""
	}
	if alias, ok := r.outgoing[name]; ok {
		return alias, ""
	}
	return name, ""
}

// AntigravityCLIResponseRestorer maps CLI tool calls of an upstream response back
// to the client's tools. It is stateless: each call is judged on its own event.
type AntigravityCLIResponseRestorer struct {
	routes *AntigravityCLIToolRoutes
}

// NewResponseRestorer returns a restorer for one response, or nil when no route applies.
func (r *AntigravityCLIToolRoutes) NewResponseRestorer() *AntigravityCLIResponseRestorer {
	if r == nil || len(r.incoming) == 0 {
		return nil
	}
	return &AntigravityCLIResponseRestorer{routes: r}
}

// Restore rewrites the function calls in one complete response JSON document or SSE
// event. Input that holds nothing to rewrite, or is not a complete JSON document,
// is returned unchanged, as is the input slice itself.
//
// A contract call is converted only when the event carries it whole: one name, one
// complete argument object, valid under the contract. Anything else, such as
// fragmented arguments or arguments outside the contract, stays under its CLI
// name. The client declares no such tool and so cannot run it; guessing at such a
// call could execute an operation the model did not issue.
func (s *AntigravityCLIResponseRestorer) Restore(data []byte) []byte {
	// The escape check keeps a member name spelled with \u escapes on the parsed path.
	if s == nil || (!bytes.Contains(data, []byte(`"functionCall"`)) && !bytes.Contains(data, []byte(`\u`))) {
		return data
	}
	payload := data
	prefix := []byte(nil)
	if trimmed := bytes.TrimSpace(data); bytes.HasPrefix(trimmed, []byte("data:")) {
		payload = bytes.TrimSpace(trimmed[len("data:"):])
		prefix = []byte("data: ")
	}
	if !ojson.ValidBytes(payload) {
		return data
	}
	doc := ojson.NewDoc(payload)
	type edit struct{ path, raw string }
	var edits []edit
	// A repeated member anywhere on the way to a call makes it ambiguous which
	// call the client's parser sees; such a response stays as it is.
	if antigravityCLIHasRepeatedMember(ojson.ParseBytes(payload)) || antigravityCLIHasRepeatedMember(doc.Get("response")) {
		return data
	}
	for _, root := range [...]string{"response.candidates", "candidates"} {
		for ci, candidate := range doc.Get(root).Array() {
			if antigravityCLIHasRepeatedMember(candidate) || antigravityCLIHasRepeatedMember(candidate.Get("content")) {
				continue
			}
			for pi, part := range candidate.Get("content.parts").Array() {
				// A part naming two calls is ambiguous and stays as it is.
				if antigravityCLIHasRepeatedMember(part) {
					continue
				}
				if restored, ok := s.restoreCall(part.Get("functionCall")); ok {
					edits = append(edits, edit{
						path: root + "." + strconv.Itoa(ci) + ".content.parts." + strconv.Itoa(pi) + ".functionCall",
						raw:  restored,
					})
				}
			}
		}
	}
	if len(edits) == 0 {
		return data
	}
	for _, item := range edits {
		_ = doc.SetRaw(item.path, item.raw)
	}
	return doc.AppendTo(append(make([]byte, 0, len(prefix)+doc.Len()), prefix...))
}

// restoreCall returns the client form of one functionCall object, or false when
// the call is not routed or must stay as it is.
func (s *AntigravityCLIResponseRestorer) restoreCall(call ojson.Result) (string, bool) {
	original, routed := s.routes.incoming[call.Get("name").String()]
	if !routed || !call.IsObject() || antigravityCLIHasRepeatedMember(call) {
		return "", false
	}
	rewritten := ojson.NewDocString(strings.Clone(call.Raw))
	_ = rewritten.Set("name", original)
	if route := s.routes.contracts[original]; route != nil {
		// Any fragment marker, under either spelling, means the arguments are
		// not known to be whole.
		for _, marker := range [...]string{"partialArgs", "partial_args", "willContinue", "will_continue"} {
			if call.Get(marker).Exists() {
				return "", false
			}
		}
		args, ok := route.convert(call.Get("args"), route.contract.decode, nil)
		if !ok {
			return "", false
		}
		_ = rewritten.SetRaw("args", args)
	}
	return rewritten.String(), true
}

// antigravityCLIHasRepeatedMember reports whether a JSON object names a member
// twice. Parsers disagree on which value wins, so rewriting one of them could
// change what the client runs.
func antigravityCLIHasRepeatedMember(object ojson.Result) bool {
	seen := map[string]struct{}{}
	repeated := false
	object.ForEach(func(key, _ ojson.Result) bool {
		_, repeated = seen[key.String()]
		seen[key.String()] = struct{}{}
		return !repeated
	})
	return repeated
}

// declaration returns the CLI tool's declaration, extended with the client's
// intent field when the client tool declares one.
func (route *antigravityCLIContractRoute) declaration() string {
	properties := route.contract.properties
	required := route.contract.required
	if route.intentSchema != "" {
		properties = append(properties[:len(properties):len(properties)], antigravityCLIField{"i", route.intentSchema})
		if route.intentRequired {
			required = append(required[:len(required):len(required)], "i")
		}
	}
	quoted := make([]string, len(required))
	for i, key := range required {
		quoted[i] = antigravityCLIJSONString(key)
	}
	return antigravityCLIObject([]antigravityCLIField{
		{"name", antigravityCLIJSONString(route.contract.nativeName)},
		{"description", antigravityCLIJSONString(route.contract.description)},
		{"parameters", antigravityCLIObject([]antigravityCLIField{
			{"type", `"OBJECT"`},
			{"properties", antigravityCLIObject(properties)},
			{"required", "[" + strings.Join(quoted, ",") + "]"},
		})},
	})
}

// convert runs one direction of the contract and carries the intent field across.
// When verify is set, the converted arguments must also pass the opposite
// direction, so only an operation the CLI tool could have issued is replayed.
func (route *antigravityCLIContractRoute) convert(args ojson.Result, convert, verify func(ojson.Result) ([]antigravityCLIField, bool)) (string, bool) {
	if !args.IsObject() {
		return "", false
	}
	intent := args.Get("i")
	switch {
	case route.intentSchema == "" && intent.Exists():
		return "", false
	case !intent.Exists() && route.intentRequired:
		return "", false
	case intent.Exists() && intent.Type != ojson.String:
		return "", false
	}
	fields, ok := convert(args)
	if !ok {
		return "", false
	}
	if verify != nil {
		if _, okVerify := verify(ojson.Parse(antigravityCLIObject(fields))); !okVerify {
			return "", false
		}
	}
	if intent.Exists() {
		fields = append(fields, antigravityCLIField{"i", strings.Clone(intent.Raw)})
	}
	return antigravityCLIObject(fields), true
}
