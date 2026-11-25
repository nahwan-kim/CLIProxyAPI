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

	antigravityCLIDefaultIdentity = "You are omp's trusted coding assistant."
	antigravityCLIEngineering     = "# Engineering"
	antigravityCLIDelegatedScope  = "## Delegated execution"
)

// antigravityCLIProfileTags wrap the behavior section when the client sends a
// per-model profile instead of its default one (omp-superkit and its predecessor;
// github.com/dbc-hbin/antigravity-omp f8a51d3 treats both the same way).
var antigravityCLIProfileTags = [...]string{"omp-model-preset", "omo-model-preset"}

// antigravityCLIBehavior is the client-owned behavior section that opens a system
// prompt, described by what the preset keeps around it.
type antigravityCLIBehavior struct {
	head      string // Role heading before the section, if any
	scope     string // delegated-execution rules of a tagged profile, kept inside the preset
	tail      string // everything after the section, from its line break on
	lineBreak string // "\n" or "\r\n", as the prompt's own lines end
}

// antigravityCLIPresetText replaces the behavior section that opens omp's system
// prompt with the preset. It reports false for any other text: custom prompts
// and policy documents that merely use the same section headings stay with their
// authors, as does a client that already applied the preset, whose prompt opens
// with neither shape. The preset's tag quoted later in the prompt changes nothing.
func antigravityCLIPresetText(text string, hasBash bool) (string, bool) {
	behavior, ok := antigravityCLIFindBehavior(text)
	if !ok {
		return text, false
	}
	sections := []string{antigravityCLIPresetBody, antigravityCLIPresetParallel}
	if hasBash {
		sections = append(sections, antigravityCLIPresetShell)
	}
	sections = append(sections, antigravityCLIPresetRefusals)
	preset := antigravityCLIPresetOpen + "\n" + strings.Join(sections, "\n\n")
	closing := "\n" + antigravityCLIPresetClose
	if behavior.lineBreak != "\n" {
		preset = strings.ReplaceAll(preset, "\n", behavior.lineBreak)
		closing = strings.ReplaceAll(closing, "\n", behavior.lineBreak)
	}
	return behavior.head + preset + behavior.scope + closing + behavior.tail, true
}

// antigravityCLIFindBehavior recognizes the two shapes omp gives that section. The
// default template runs from omp's own identity line up to the Runtime section.
// A tagged profile runs to its closing tag, whatever it says inside, and only
// its delegated-execution rules outlive it. Either must open the text, after an
// optional Role heading, and be followed by the default template's host sections.
// The lines that give the prompt its structure share one line break, read off
// the first line.
func antigravityCLIFindBehavior(text string) (antigravityCLIBehavior, bool) {
	nl := "\n"
	if at := strings.IndexByte(text, '\n'); at > 0 && text[at-1] == '\r' {
		nl = "\r\n"
	}
	head, marker := "", "⁂"
	for _, candidate := range [...]string{"⁂", "§"} {
		if heading := candidate + " Role" + nl; strings.HasPrefix(text, heading) {
			head, marker = heading, candidate
		}
	}
	body := text[len(head):]
	for _, tag := range antigravityCLIProfileTags {
		open, closing := "<"+tag+">", nl+"</"+tag+">"+nl
		if !strings.HasPrefix(body, open+nl) {
			continue
		}
		// The line break that ends the opening tag may also start the closing one.
		profile := body[len(open):]
		end := strings.Index(profile, closing)
		if end < 0 {
			return antigravityCLIBehavior{}, false
		}
		// The tail starts at the line break that ends the closing tag.
		after := len(head) + len(open) + end + len(closing) - len(nl)
		if antigravityCLIHostSections(text, after, marker, nl) < 0 {
			return antigravityCLIBehavior{}, false
		}
		scope := ""
		if at := strings.Index(profile[:end], nl+nl+antigravityCLIDelegatedScope+nl); at >= 0 {
			scope = profile[at:end]
		}
		return antigravityCLIBehavior{head: head, scope: scope, tail: text[after:], lineBreak: nl}, true
	}
	opening := antigravityCLIDefaultIdentity + nl
	if head == "" {
		opening += nl + antigravityCLIEngineering + nl
	}
	if !strings.HasPrefix(body, opening) {
		return antigravityCLIBehavior{}, false
	}
	runtime := antigravityCLIHostSections(text, len(head), marker, nl)
	if runtime < 0 {
		return antigravityCLIBehavior{}, false
	}
	return antigravityCLIBehavior{head: head, tail: nl + text[runtime:], lineBreak: nl}, true
}

// antigravityCLIHostSections returns the index of the line break before the
// Runtime heading at or after from, or -1 unless the landmarks that identify
// the default template follow in the same order.
func antigravityCLIHostSections(text string, from int, marker, nl string) int {
	at, runtime := from, -1
	for i, section := range [...]string{"Runtime", "Tool Policy", "Workflow"} {
		next := strings.Index(text[at:], nl+marker+" "+section+nl)
		if next < 0 {
			return -1
		}
		at += next
		if i == 0 {
			runtime = at
		}
	}
	return runtime
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
