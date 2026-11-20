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

func TestAntigravityCLIPresetLeavesOtherPromptsToTheirAuthors(t *testing.T) {
	for name, tc := range map[string]struct{ model, system string }{
		"custom template":                     {"gemini-3-flash", "You are a release bot.\n⁂ Runtime\n⁂ Tool Policy\n⁂ Workflow\n"},
		"policy document using the headings":  {"gemini-3-flash", "Repository release policy\n§ Role\nReview only. Do not write files or run shell commands.\n§ Runtime\n§ Tool Policy\n§ Workflow\n"},
		"role heading without omp's identity": {"gemini-3-flash", "⁂ Role\nReview only.\n⁂ Runtime\n⁂ Tool Policy\n⁂ Workflow\n"},
		"sections out of order":               {"gemini-3-flash", "You are omp's trusted coding assistant.\n\n# Engineering\n\n⁂ Workflow\n⁂ Runtime\n"},
		"preset already applied":              {"gemini-3-flash", antigravityCLIPresetOpen + "\nclient preset\n" + antigravityCLIPresetClose + antigravityCLITestHostTail},
		"non-Gemini model":                    {"claude-sonnet-4-6", "You are a careful assistant.\n\n# Engineering\n" + antigravityCLITestHostTail},
	} {
		t.Run(name, func(t *testing.T) {
			if got := antigravityCLITestSystemPrompt(t, tc.model, tc.system); got != tc.system {
				t.Fatalf("system prompt changed: %q", got)
			}
		})
	}
}
