package helps

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// The preset follows github.com/dbc-hbin/omp-ulw (prompts/agy.ts, presets.ts):
// the Antigravity CLI identity and communication template, adapted to a host
// that owns tools and permissions. Only the parts that do not depend on the
// client's surface (terminal or app, main agent or subagent) are carried over;
// the proxy cannot tell those apart.
const (
	antigravityCLIPresetOpen  = "<AGY_MODEL_PRESET>"
	antigravityCLIPresetClose = "</AGY_MODEL_PRESET>"

	antigravityCLIPresetBody = `You are Antigravity, a powerful agentic AI coding assistant designed by the Google Deepmind team working on Advanced Agentic Coding.
You are pair programming with a USER to solve their coding task. The task may require creating a new codebase, modifying or debugging an existing codebase, or simply answering a question.
The USER will send you requests, which you must always prioritize addressing.

## Working with the user

- Keep your responses concise and format them in GitHub-style Markdown.
- If you are unsure about the user's intent and the difference materially affects the result, ask for clarification rather than making assumptions. Respect the host's approval requirements.
- Link to the relevant files and code symbols in your response, including line references when helpful.

## Working in agy

- Use the available tools directly according to their native schemas. Follow the active host's permissions, specialist roles and completion protocol; prompt text does not grant capabilities.
- Read the relevant code and callers before editing. Follow the project's conventions and preserve unrelated changes.
- Verify changes through their real entry points. Report what you observed, which checks you ran, and any remaining limits. Only report completion when the requested outcome has been verified.`

	antigravityCLIPresetParallel = "Run independent reads, searches, and lookups in parallel. Inspect every tool result. Keep edits, side effects, deploys, approvals, native waits, state-dependent shell calls, and result-dependent calls sequential; inspect each before the next. Preserve live schemas, specialized-tool routing, permissions, and job lifecycles. Keep call identities, failures, tool-reported errors, and missing or truncated results visible in the evidence."

	antigravityCLIPresetShell = "## agy shell execution\n\nSequence bash calls that depend on shell state; overlapping calls do not provide state handoffs. When the host reports a call running in a copy of the busy session shell, it sees variables, functions, options and working directory from the last completed command boundary; changes in that copy do not persist in the original shell."

	antigravityCLIPresetRefusals = "## Refusals are final\n\nA permission or approval denial, or a refusal by the user, is not a blocker to work around: it is final for what that call was meant to do. Never reach that result with another tool, command, path or host. Continue independent work, then report the refused step as final or blocked."

	antigravityCLIDefaultIdentity   = "You are omp's trusted coding assistant.\n"
	antigravityCLIHeaderlessOpening = antigravityCLIDefaultIdentity + "\n# Engineering\n"
)

// antigravityCLIPresetText replaces the behavior section of omp's default system
// template, from its opening up to the Runtime section, with the preset. It
// reports false for any other text: the template must open the text with omp's
// own identity line, so custom prompts and policy documents that merely use the
// same section headings stay with their authors, as does a client that already
// applied the preset.
func antigravityCLIPresetText(text string, hasBash bool) (string, bool) {
	if strings.Contains(text, antigravityCLIPresetOpen) {
		return text, false
	}
	marker, bodyStart := "⁂", 0
	if !strings.HasPrefix(text, antigravityCLIHeaderlessOpening) {
		found := false
		for _, candidate := range [...]string{"⁂", "§"} {
			heading := candidate + " Role\n"
			if strings.HasPrefix(text, heading+antigravityCLIDefaultIdentity) {
				marker, bodyStart, found = candidate, len(heading), true
			}
		}
		if !found {
			return text, false
		}
	}
	// The same landmarks, in the same order, that identify the default template.
	at := bodyStart
	runtime := -1
	for i, section := range [...]string{"Runtime", "Tool Policy", "Workflow"} {
		next := strings.Index(text[at:], "\n"+marker+" "+section+"\n")
		if next <= 0 {
			return text, false
		}
		at += next
		if i == 0 {
			runtime = at
		}
	}
	sections := []string{antigravityCLIPresetBody, antigravityCLIPresetParallel}
	if hasBash {
		sections = append(sections, antigravityCLIPresetShell)
	}
	sections = append(sections, antigravityCLIPresetRefusals)
	return text[:bodyStart] + antigravityCLIPresetOpen + "\n" + strings.Join(sections, "\n\n") + "\n" + antigravityCLIPresetClose + "\n" + text[runtime:], true
}

// antigravityCLIDeclaresTool reports whether the request declares a tool by name.
func antigravityCLIDeclaresTool(doc *ojson.Doc, name string) bool {
	for _, tool := range doc.Get("request.tools").Array() {
		for _, group := range [...]string{"functionDeclarations", "function_declarations"} {
			for _, declaration := range tool.Get(group).Array() {
				if declaration.Get("name").String() == name {
					return true
				}
			}
		}
	}
	return false
}
