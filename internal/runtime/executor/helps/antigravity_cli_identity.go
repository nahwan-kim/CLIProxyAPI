package helps

import (
	"crypto/sha256"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/MoeForge/moego/regexp"
	"github.com/google/uuid"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const (
	// AntigravityCLIUserAgent is the consumer identity of the Antigravity CLI 1.5.9.
	AntigravityCLIUserAgent = "antigravity/cli/1.5.9 (aidev_client; os_type=darwin; arrm64; cl=1021519450; auth_method=consumer)"

	// antigravityCLISessionID is the signed decimal FNV-1a 64 offset basis, which the CLI
	// sends because it hashes an empty session-source slot.
	antigravityCLISessionID = "-3750763034362895579"

	antigravityClientIdentityKey = "client_identity"
	antigravityClientIdentityCLI = "cli"
)

var (
	// antigravityCLIBlockedSentences are the sentences of omp's own prompt that Google
	// answers with 429 RESOURCE_EXHAUSTED when a system prompt carries one verbatim,
	// wherever it stands in the text, each paired with wording that keeps its meaning.
	// The match is exact: the same sentence without its period or with one word moved
	// passes, and so does the sentence inside a tool declaration.
	antigravityCLIBlockedSentences = strings.NewReplacer(
		antigravityCLIDefaultIdentity,
		"You are Antigravity's trusted coding assistant.",
		"Touch displayed changed lines only; `…`, `..`, collapsed `N-M:` and out-of-window lines UNSEEN.",
		"Edit only the changed lines that were displayed; treat `…`, `..`, collapsed `N-M:` ranges and lines outside the shown window as not seen.",
	)
	antigravityCLIPresetBranding  = regexp.MustCompile(`(?m)^You are omp(, a coding agent[. ])`)
	antigravityCLIHeadingBranding = regexp.MustCompile(`(?m)^(## )OMP (todo updates|shell execution)(\r?)$`)
)

// AntigravityCLIIdentityEnabled reports whether the credential opted into the CLI
// request identity with `client_identity: cli`.
func AntigravityCLIIdentityEnabled(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return false
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes[antigravityClientIdentityKey]); v != "" {
			return strings.EqualFold(v, antigravityClientIdentityCLI)
		}
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata[antigravityClientIdentityKey].(string); ok {
			return strings.EqualFold(strings.TrimSpace(v), antigravityClientIdentityCLI)
		}
	}
	return false
}

// ApplyAntigravityCLIIdentityDoc rewrites an agent request into the CLI's wire shape:
// envelope ids and labels, per-declaration tool entries, CLI tool names and
// arguments, and the system-prompt identity line. Image and web-search requests are
// left untouched.
//
// The returned value, nil when the request is not an agent request, restores the
// client's view of the tool data after the upstream body has been serialized.
// Credit opt-in set by the executor is kept: unlike the CLI, the proxy has an
// explicit source for it.
func ApplyAntigravityCLIIdentityDoc(doc *ojson.Doc, modelName string) *AntigravityCLIRequestTools {
	if doc == nil || doc.Get("requestType").String() != "agent" {
		return nil
	}
	applyAntigravityCLIEnvelopeDoc(doc, modelName)

	_ = doc.Delete("userPromptId")
	for _, key := range [...]string{"temperature", "topP", "topK", "presencePenalty"} {
		_ = doc.Delete("request.generationConfig." + key)
	}
	if doc.Get("request.systemInstruction").Exists() {
		SetStringIfDifferentDoc(doc, "request.systemInstruction.role", "user")
	}
	applyAntigravityCLIBrandingDoc(doc, modelName)

	normalizeAntigravityCLIToolSpellingsDoc(doc)
	return newAntigravityCLIToolRoutes(doc).applyRequestDoc(doc)
}

func applyAntigravityCLIEnvelopeDoc(doc *ojson.Doc, modelName string) {
	// The proxy is stateless, so conversation ids derive from the stable session id
	// the executor already computed (session affinity or first user message).
	seed := doc.Get("request.sessionId").String()
	cascadeID := antigravityCLIDerivedUUID(seed, "cascade")
	trajectoryID := antigravityCLIDerivedUUID(seed, "trajectory")

	// Each user turn, model turn and tool result is one step; the request index
	// counts the model turns already answered in this trajectory.
	step, requestIndex := 0, 0
	for _, content := range doc.Get("request.contents").Array() {
		step++
		if content.Get("role").String() == "model" {
			requestIndex++
		}
	}
	if step == 0 {
		step = 1
	}

	labels := map[string]string{}
	doc.Get("request.labels").ForEach(func(key, value ojson.Result) bool {
		labels[strings.Clone(key.String())] = strings.Clone(value.String())
		return true
	})
	isClaude := strings.Contains(modelName, "claude")
	labels["cascade_id"] = cascadeID
	labels["root_cascade_id"] = cascadeID
	labels["trajectory_id"] = trajectoryID
	labels["request_id"] = trajectoryID + "-" + strconv.Itoa(requestIndex)
	labels["last_step_index"] = strconv.Itoa(step - 1)
	labels["used_claude"] = strconv.FormatBool(isClaude)
	labels["used_claude_conservative"] = strconv.FormatBool(isClaude)
	labels["used_non_gemini_model"] = strconv.FormatBool(!strings.Contains(modelName, "gemini"))

	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fields := make([]antigravityCLIField, len(keys))
	for i, key := range keys {
		fields[i] = antigravityCLIField{key: key, raw: antigravityCLIJSONString(labels[key])}
	}
	_ = doc.SetRaw("request.labels", antigravityCLIObject(fields))

	_ = doc.Set("request.sessionId", antigravityCLISessionID)
	_ = doc.Set("requestId", "agent/"+cascadeID+"/"+strconv.FormatInt(time.Now().UnixMilli(), 10)+"/"+trajectoryID+"/"+strconv.Itoa(step))
}

func antigravityCLIDerivedUUID(seed, purpose string) string {
	if seed == "" {
		return uuid.NewString()
	}
	// NewHash stamps the version-4 and variant bits onto the digest.
	return uuid.NewHash(sha256.New(), uuid.Nil, []byte("cli-proxy-api:antigravity:cli-"+purpose+"\x00"+seed), 4).String()
}

// applyAntigravityCLIBrandingDoc gives Gemini models the Antigravity preset in
// place of omp's default behavior section. Every model, recognized prompt or
// not, has the sentences Google rejects reworded and its identity lines renamed.
func applyAntigravityCLIBrandingDoc(doc *ojson.Doc, modelName string) {
	type edit struct{ path, text string }
	var edits []edit
	gemini := strings.Contains(strings.ToLower(modelName), "gemini")
	hasBash := gemini && antigravityCLIDeclaresTool(doc, "bash")
	for i, part := range doc.Get("request.systemInstruction.parts").Array() {
		text := part.Get("text")
		if text.Type != ojson.String {
			continue
		}
		original := text.String()
		branded := original
		if gemini {
			branded, _ = antigravityCLIPresetText(branded, hasBash)
		}
		branded = antigravityCLIBlockedSentences.Replace(branded)
		branded = antigravityCLIPresetBranding.ReplaceAllString(branded, "You are Antigravity${1}")
		branded = antigravityCLIHeadingBranding.ReplaceAllString(branded, "${1}Antigravity ${2}${3}")
		if branded != original {
			edits = append(edits, edit{path: "request.systemInstruction.parts." + strconv.Itoa(i) + ".text", text: branded})
		}
	}
	for _, item := range edits {
		_ = doc.Set(item.path, item.text)
	}
}

// antigravityCLIField is one member of a JSON object under construction; raw is
// already valid JSON.
type antigravityCLIField struct{ key, raw string }

func antigravityCLIObject(fields []antigravityCLIField) string {
	var raw strings.Builder
	raw.WriteByte('{')
	for i, field := range fields {
		if i > 0 {
			raw.WriteByte(',')
		}
		raw.WriteString(antigravityCLIJSONString(field.key))
		raw.WriteByte(':')
		raw.WriteString(field.raw)
	}
	raw.WriteByte('}')
	return raw.String()
}

func antigravityCLIJSONString(value string) string {
	quoted, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return `""`
	}
	return string(quoted)
}
