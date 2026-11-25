package helps

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

const antigravityCLITestHostTail = "\n⁂ Runtime\nskill://x, omp paths\n⁂ Tool Policy\n# General\n⁂ Workflow\n# 1. Before Editing\n"

func antigravityCLITestSystemPrompt(t *testing.T, model, system string) string {
	t.Helper()
	doc := ojson.NewDocString(`{"requestType":"agent","request":{"sessionId":"-1","tools":[{"functionDeclarations":[{"name":"bash"}]}]}}`)
	_ = doc.Set("request.systemInstruction.parts.0.text", system)
	ApplyAntigravityCLIIdentityDoc(doc, model)
	return doc.Get("request.systemInstruction.parts.0.text").String()
}

func TestAntigravityCLIPresetReplacesOnlyTheDefaultBehaviorSection(t *testing.T) {
	for name, opening := range map[string]string{
		"headerless template": "You are omp's trusted coding assistant.\n\n# Engineering\n- Correctness first.\n",
		"role heading":        "⁂ Role\nYou are omp's trusted coding assistant.\n# Engineering\n",
	} {
		t.Run(name, func(t *testing.T) {
			got := antigravityCLITestSystemPrompt(t, "gemini-3-flash", opening+antigravityCLITestHostTail)
			if !strings.HasSuffix(got, antigravityCLIPresetClose+"\n"+antigravityCLITestHostTail) {
				t.Fatalf("host sections after the behavior section changed: %q", got)
			}
			if strings.Contains(got, "omp's trusted") || strings.Contains(got, "# Engineering") {
				t.Fatalf("default behavior section survived: %q", got)
			}
			if !strings.Contains(got, antigravityCLIPresetOpen+"\nYou are Antigravity, a powerful agentic AI coding assistant") || !strings.Contains(got, "## agy shell execution") {
				t.Fatalf("preset missing: %q", got)
			}
		})
	}
}

// A document quoted after the template may use the other marker; only the
// template that opens the text is replaced and everything after its Runtime
// heading is kept.
func TestAntigravityCLIPresetKeepsEverythingAfterRuntime(t *testing.T) {
	tail := "\n§ Runtime\n§ Tool Policy\n§ Workflow\nHandbook:\n⁂ Role\nquoted role\n⁂ Runtime\n⁂ Tool Policy\n⁂ Workflow\n"
	got := antigravityCLITestSystemPrompt(t, "gemini-3-flash", "§ Role\nYou are omp's trusted coding assistant.\n# Engineering\n"+tail)
	if !strings.HasPrefix(got, "§ Role\n"+antigravityCLIPresetOpen) || !strings.HasSuffix(got, antigravityCLIPresetClose+"\n"+tail) {
		t.Fatalf("system prompt = %q", got)
	}
}

// omp-superkit sends its per-model profile between tags instead of the default
// template. Google rejects the request when that profile reaches it.
func TestAntigravityCLIPresetReplacesTaggedProfile(t *testing.T) {
	const delegated = "\n\n## Delegated execution\n\nYou are a subagent, not the main agent."
	for name, tc := range map[string]struct{ head, tag, profile, scope string }{
		"main agent profile":      {"", "omp-model-preset", "You are omp's trusted coding assistant.\n\n# Engineering\n- Correctness first.", ""},
		"subagent profile":        {"", "omp-model-preset", "You are omp's trusted coding assistant.\n\n# Engineering\n- Correctness first." + delegated, delegated},
		"under a role heading":    {"⁂ Role\n", "omp-model-preset", "You are omp's trusted coding assistant.\n\n# Engineering", ""},
		"profile of another kind": {"", "omp-model-preset", "You are omp, tuned for long tasks.\n\n# Method\n- Plan first.", ""},
		"predecessor tag":         {"", "omo-model-preset", "You are omp's trusted coding assistant.\n\n# Engineering", ""},
	} {
		t.Run(name, func(t *testing.T) {
			system := tc.head + "<" + tc.tag + ">\n" + tc.profile + "\n</" + tc.tag + ">\n" + antigravityCLITestHostTail
			got := antigravityCLITestSystemPrompt(t, "gemini-3-flash", system)
			if !strings.HasPrefix(got, tc.head+antigravityCLIPresetOpen+"\nYou are Antigravity, a powerful agentic AI coding assistant") {
				t.Fatalf("preset does not open the prompt: %q", got)
			}
			if !strings.HasSuffix(got, antigravityCLIPresetRefusals+tc.scope+"\n"+antigravityCLIPresetClose+"\n"+antigravityCLITestHostTail) {
				t.Fatalf("preset end, delegated rules or host sections changed: %q", got)
			}
			if strings.Contains(got, tc.tag) || strings.Contains(got, "You are omp") || strings.Contains(got, "\n# Engineering") || strings.Contains(got, "\n# Method") {
				t.Fatalf("client profile survived: %q", got)
			}
		})
	}
}

// A prompt written with CRLF line breaks is recognized like its LF twin, and the
// preset takes the prompt's line break.
func TestAntigravityCLIPresetFollowsThePromptLineBreak(t *testing.T) {
	crlf := func(text string) string { return strings.ReplaceAll(text, "\n", "\r\n") }
	for name, system := range map[string]string{
		"headerless template":     "You are omp's trusted coding assistant.\n\n# Engineering\n- Correctness first.\n" + antigravityCLITestHostTail,
		"role heading":            "§ Role\nYou are omp's trusted coding assistant.\n# Engineering\n\n§ Runtime\n§ Tool Policy\n§ Workflow\n",
		"tagged subagent profile": "<omp-model-preset>\nYou are omp's trusted coding assistant.\n\n## Delegated execution\n\nYou are a subagent.\n</omp-model-preset>\n" + antigravityCLITestHostTail,
	} {
		t.Run(name, func(t *testing.T) {
			want := crlf(antigravityCLITestSystemPrompt(t, "gemini-3-flash", system))
			got := antigravityCLITestSystemPrompt(t, "gemini-3-flash", crlf(system))
			if got == crlf(system) {
				t.Fatalf("CRLF prompt was not recognized: %q", got)
			}
			if got != want {
				t.Fatalf("CRLF result differs from the LF result beyond line breaks:\n got %q\nwant %q", got, want)
			}
		})
	}
}

func TestAntigravityCLIPresetLeavesOtherPromptsToTheirAuthors(t *testing.T) {
	for name, tc := range map[string]struct{ model, system string }{
		"custom template":                      {"gemini-3-flash", "You are a release bot.\n⁂ Runtime\n⁂ Tool Policy\n⁂ Workflow\n"},
		"policy document using the headings":   {"gemini-3-flash", "Repository release policy\n§ Role\nReview only. Do not write files or run shell commands.\n§ Runtime\n§ Tool Policy\n§ Workflow\n"},
		"role heading without omp's identity":  {"gemini-3-flash", "⁂ Role\nReview only.\n⁂ Runtime\n⁂ Tool Policy\n⁂ Workflow\n"},
		"sections out of order":                {"gemini-3-flash", "You are omp's trusted coding assistant.\n\n# Engineering\n\n⁂ Workflow\n⁂ Runtime\n"},
		"preset already applied":               {"gemini-3-flash", antigravityCLIPresetOpen + "\nclient preset\n" + antigravityCLIPresetClose + antigravityCLITestHostTail},
		"non-Gemini model":                     {"claude-sonnet-4-6", "You are a careful assistant.\n\n# Engineering\n" + antigravityCLITestHostTail},
		"tagged profile left open":             {"gemini-3-flash", "<omp-model-preset>\nYou are omp's trusted coding assistant.\n\n# Engineering\n" + antigravityCLITestHostTail},
		"tagged profile closed mid-line":       {"gemini-3-flash", "<omp-model-preset>\nKeep the tag </omp-model-preset> in replies.\n" + antigravityCLITestHostTail},
		"tagged profile quoted in a document":  {"gemini-3-flash", "Handbook\n<omp-model-preset>\nYou are omp's trusted coding assistant.\n</omp-model-preset>\n" + antigravityCLITestHostTail},
		"tagged profile without host sections": {"gemini-3-flash", "<omp-model-preset>\nYou are omp's trusted coding assistant.\n</omp-model-preset>\nReview only.\n"},
		"tagged profile after host sections":   {"gemini-3-flash", "<omp-model-preset>\nprofile" + antigravityCLITestHostTail + "</omp-model-preset>\n"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := antigravityCLITestSystemPrompt(t, tc.model, tc.system); got != tc.system {
				t.Fatalf("system prompt changed: %q", got)
			}
		})
	}
}
