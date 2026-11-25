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

// A document quoted after the template may use the other marker or the preset's
// own tag; only the template that opens the text is replaced and everything
// after its Runtime heading is kept.
func TestAntigravityCLIPresetKeepsEverythingAfterRuntime(t *testing.T) {
	tail := "\n§ Runtime\n§ Tool Policy\n§ Workflow\nHandbook:\n⁂ Role\nquoted role\n⁂ Runtime\n⁂ Tool Policy\n⁂ Workflow\nThe proxy wraps its preset in " + antigravityCLIPresetOpen + ".\n"
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

// Google answers 429 when a system prompt carries one of omp's own sentences
// verbatim, wherever it stands. The sentence is reworded in place, on every
// model, and text that merely resembles it is left alone.
func TestAntigravityCLIBlockedSentencesDoNotReachTheSystemPrompt(t *testing.T) {
	const editRule = "Touch displayed changed lines only; `…`, `..`, collapsed `N-M:` and out-of-window lines UNSEEN."
	for name, tc := range map[string]struct{ model, before, sentence, after string }{
		"edit rule in the inline tool inventory": {"gemini-3-flash", "## functions\n\n// <rules>\n// - ", editRule, " Re-read first.\n// </rules>\ntype edit = (_: {\ninput: string,\n});\n"},
		"identity quoted in a memory note":       {"gemini-3-flash", "You are a release bot.\n# Memory\n- The line `", antigravityCLIDefaultIdentity, "` was rejected upstream.\n"},
		"another model on the same credential":   {"claude-sonnet-4-6", "You are a careful assistant.\n- ", editRule, "\n"},
	} {
		t.Run(name, func(t *testing.T) {
			got := antigravityCLITestSystemPrompt(t, tc.model, tc.before+tc.sentence+tc.after)
			if strings.Contains(got, tc.sentence) {
				t.Fatalf("blocked sentence survived: %q", got)
			}
			if !strings.HasPrefix(got, tc.before) || !strings.HasSuffix(got, tc.after) || len(got) <= len(tc.before)+len(tc.after) {
				t.Fatalf("text around the sentence changed, or the sentence was dropped instead of reworded: %q", got)
			}
		})
	}
	const nearMisses = "You are omp's trusted coding assistant\n- Touch only displayed changed lines; `…`, `..`, collapsed `N-M:` and out-of-window lines UNSEEN.\n"
	if got := antigravityCLITestSystemPrompt(t, "gemini-3-flash", nearMisses); got != nearMisses {
		t.Fatalf("text that only resembles a blocked sentence changed: %q", got)
	}
	// The main agent's prompt: the preset replaces the behavior section and the
	// inline tool inventory after it still carries the rule.
	const ruleTail = " Re-read first.\n"
	got := antigravityCLITestSystemPrompt(t, "gemini-3-flash", "You are omp's trusted coding assistant.\n\n# Engineering\n"+antigravityCLITestHostTail+"// - "+editRule+ruleTail)
	if !strings.HasPrefix(got, antigravityCLIPresetOpen) || strings.Contains(got, editRule) || !strings.HasSuffix(got, ruleTail) {
		t.Fatalf("blocked sentence survived after the preset, or the text after it changed: %q", got)
	}
}

// Apart from the sentence Google rejects, which is reworded where it stands,
// these prompts are not omp's to rewrite.
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
			got := antigravityCLITestSystemPrompt(t, tc.model, tc.system)
			before, after, blocked := strings.Cut(tc.system, antigravityCLIDefaultIdentity)
			if !blocked {
				if got != tc.system {
					t.Fatalf("system prompt changed: %q", got)
				}
				return
			}
			if !strings.HasPrefix(got, before) || !strings.HasSuffix(got, after) || len(got) <= len(before)+len(after) {
				t.Fatalf("text around the blocked sentence changed: %q", got)
			}
			if reworded := got[len(before) : len(got)-len(after)]; reworded == antigravityCLIDefaultIdentity || strings.Contains(reworded, "\n") {
				t.Fatalf("blocked sentence was not reworded in place: %q", reworded)
			}
		})
	}
}
