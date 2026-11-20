package helps

import (
	"math"
	pathpkg "path"
	"strconv"
	"strings"
	"unicode"

	"github.com/MoeForge/moego/ojson"
	"github.com/MoeForge/moego/regexp"
)

// antigravityCLIToolAliases maps client tool names to the names the CLI declares
// for tools whose arguments pass through unchanged.
var antigravityCLIToolAliases = [...][2]string{
	{"bash", "run_command"},
	{"edit", "edit_file"},
	{"task", "invoke_subagent"},
	{"web_search", "search_web"},
	{"ask", "ask_question"},
	{"apply_patch", "edit_file"},
}

// antigravityCLIFindLimit is the result cap of the CLI's find converter.
const antigravityCLIFindLimit = 50

var (
	antigravityCLIRelativePath  = regexp.MustCompile(`^[\p{L}\p{M}\p{N}_. /-]+$`)
	antigravityCLIReadSelector  = regexp.MustCompile(`^([^:]+):raw(?::([1-9]\d*)-([1-9]\d*)?)?$`)
	antigravityCLIRegexMeta     = regexp.MustCompile(`[.*+?^${}()|\[\]\\]`)
	antigravityCLIEscapedMeta   = regexp.MustCompile(`\\([.*+?^${}()|\[\]\\])`)
	antigravityCLIFindPattern   = regexp.MustCompile(`^[A-Za-z0-9_. *?-]+$`)
	antigravityCLIFindGlob      = regexp.MustCompile(`^(?:(.*)/)?\*\*/([^/]+)$`)
	antigravityCLICaseFoldClass = regexp.MustCompile(`\[([a-z])[A-Z]\]`)
)

const (
	antigravityCLIPathSchema       = `{"type":"STRING","pattern":"^[\\p{L}\\p{M}\\p{N}_. /-]+$"}`
	antigravityCLILineSchema       = `{"type":"INTEGER","minimum":1,"maximum":9007199254740991}`
	antigravityCLISearchPathSchema = `{"type":"STRING","description":"An existing local path inside the workspace. Use . for the workspace root."}`
)

// antigravityCLIToolContract pairs a client tool with the CLI tool of the same
// purpose and converts arguments between the two schemas. decode turns CLI
// arguments into client arguments; encode is its inverse for replayed history.
// Both ignore the client's intent field "i", which the route carries across.
type antigravityCLIToolContract struct {
	hostName     string
	nativeName   string
	description  string
	properties   []antigravityCLIField
	required     []string
	hostRequired []string
	decode       func(args ojson.Result) ([]antigravityCLIField, bool)
	encode       func(args ojson.Result) ([]antigravityCLIField, bool)
}

var antigravityCLIToolContracts = [...]*antigravityCLIToolContract{
	{
		hostName:    "read",
		nativeName:  "read_file",
		description: "Read a local file. StartLine and EndLine are one-based and inclusive.",
		properties: []antigravityCLIField{
			{"Path", antigravityCLIPathSchema},
			{"StartLine", antigravityCLILineSchema},
			{"EndLine", antigravityCLILineSchema},
		},
		required:     []string{"Path"},
		hostRequired: []string{"path"},
		decode: func(args ojson.Result) ([]antigravityCLIField, bool) {
			target := args.Get("Path")
			if !antigravityCLIOnlyKeys(args, "Path", "StartLine", "EndLine") || !antigravityCLIValidRelativePath(target) {
				return nil, false
			}
			start, hasStart, okStart := antigravityCLIOptionalLine(args.Get("StartLine"))
			end, hasEnd, okEnd := antigravityCLIOptionalLine(args.Get("EndLine"))
			if !okStart || !okEnd {
				return nil, false
			}
			selector := target.String() + ":raw"
			if hasStart || hasEnd {
				if !hasStart {
					start = 1
				}
				if hasEnd && end < start {
					return nil, false
				}
				selector += ":" + strconv.FormatInt(start, 10) + "-"
				if hasEnd {
					selector += strconv.FormatInt(end, 10)
				}
			}
			return []antigravityCLIField{{"path", antigravityCLIJSONString(selector)}}, true
		},
		encode: func(args ojson.Result) ([]antigravityCLIField, bool) {
			target := args.Get("path")
			if !antigravityCLIOnlyKeys(args, "path") || target.Type != ojson.String {
				return nil, false
			}
			match := antigravityCLIReadSelector.FindStringSubmatch(target.String())
			if match == nil {
				return nil, false
			}
			fields := []antigravityCLIField{{"Path", antigravityCLIJSONString(match[1])}}
			if match[2] != "" {
				fields = append(fields, antigravityCLIField{"StartLine", match[2]})
			}
			if match[3] != "" {
				fields = append(fields, antigravityCLIField{"EndLine", match[3]})
			}
			return fields, true
		},
	},
	{
		hostName:    "grep",
		nativeName:  "grep_search",
		description: "Search text inside the workspace. Supports single-line literal matches and returns OMP search output.",
		properties: []antigravityCLIField{
			{"SearchPath", antigravityCLISearchPathSchema},
			{"Query", `{"type":"STRING"}`},
			{"MatchPerLine", `{"type":"BOOLEAN","description":"Must be true."}`},
			{"IsRegex", `{"type":"BOOLEAN","description":"Must be false. Query is interpreted literally."}`},
			{"CaseInsensitive", `{"type":"BOOLEAN"}`},
		},
		required:     []string{"SearchPath", "Query", "MatchPerLine", "IsRegex"},
		hostRequired: []string{"path", "pattern"},
		decode: func(args ojson.Result) ([]antigravityCLIField, bool) {
			target, query, insensitive := args.Get("SearchPath"), args.Get("Query"), args.Get("CaseInsensitive")
			if !antigravityCLIOnlyKeys(args, "SearchPath", "Query", "MatchPerLine", "IsRegex", "CaseInsensitive") ||
				!antigravityCLIValidSearchPath(target) || query.Type != ojson.String ||
				args.Get("MatchPerLine").Type != ojson.True || args.Get("IsRegex").Type != ojson.False ||
				(insensitive.Exists() && !antigravityCLIIsBool(insensitive)) {
				return nil, false
			}
			text := query.String()
			if antigravityCLIIsBlank(text) || strings.ContainsAny(text, "\r\n\\\x00") {
				return nil, false
			}
			fields := []antigravityCLIField{
				{"path", antigravityCLIJSONString(target.String())},
				{"pattern", antigravityCLIJSONString(antigravityCLILiteralRegex(text))},
			}
			if insensitive.Exists() {
				fields = append(fields, antigravityCLIField{"case", strconv.FormatBool(insensitive.Type != ojson.True)})
			}
			return fields, true
		},
		encode: func(args ojson.Result) ([]antigravityCLIField, bool) {
			target, pattern, sensitive := args.Get("path"), args.Get("pattern"), args.Get("case")
			if !antigravityCLIOnlyKeys(args, "path", "pattern", "case") || target.Type != ojson.String ||
				pattern.Type != ojson.String || (sensitive.Exists() && !antigravityCLIIsBool(sensitive)) {
				return nil, false
			}
			query := antigravityCLIEscapedMeta.ReplaceAllString(pattern.String(), "${1}")
			if antigravityCLILiteralRegex(query) != pattern.String() {
				return nil, false
			}
			fields := []antigravityCLIField{
				{"SearchPath", antigravityCLIJSONString(target.String())},
				{"Query", antigravityCLIJSONString(query)},
				{"MatchPerLine", "true"},
				{"IsRegex", "false"},
			}
			if sensitive.Exists() {
				fields = append(fields, antigravityCLIField{"CaseInsensitive", strconv.FormatBool(sensitive.Type != ojson.True)})
			}
			return fields, true
		},
	},
	{
		hostName:    "glob",
		nativeName:  "find_by_name",
		description: "Find files and directories recursively by filename glob inside the workspace. Supports ASCII names, * and ?, with smart case. Returns OMP path results.",
		properties: []antigravityCLIField{
			{"SearchDirectory", antigravityCLISearchPathSchema},
			{"Pattern", `{"type":"STRING","pattern":"^[A-Za-z0-9_. *?-]+$"}`},
		},
		required:     []string{"SearchDirectory", "Pattern"},
		hostRequired: []string{"path", "limit", "gitignore", "hidden"},
		decode: func(args ojson.Result) ([]antigravityCLIField, bool) {
			directory, pattern := args.Get("SearchDirectory"), args.Get("Pattern")
			if !antigravityCLIOnlyKeys(args, "SearchDirectory", "Pattern") || !antigravityCLIValidSearchPath(directory) || pattern.Type != ojson.String {
				return nil, false
			}
			name := pattern.String()
			if !antigravityCLIFindPattern.MatchString(name) || strings.TrimSpace(name) != name || name == "." || name == ".." {
				return nil, false
			}
			return []antigravityCLIField{
				{"path", antigravityCLIJSONString(pathpkg.Join(directory.String(), "**", antigravityCLINameGlob(name)))},
				{"limit", strconv.Itoa(antigravityCLIFindLimit)},
				{"gitignore", "true"},
				{"hidden", "false"},
			}, true
		},
		encode: func(args ojson.Result) ([]antigravityCLIField, bool) {
			target, limit := args.Get("path"), args.Get("limit")
			if !antigravityCLIOnlyKeys(args, "path", "limit", "gitignore", "hidden") || target.Type != ojson.String ||
				limit.Type != ojson.Number || limit.Float() != antigravityCLIFindLimit ||
				args.Get("gitignore").Type != ojson.True || args.Get("hidden").Type != ojson.False {
				return nil, false
			}
			match := antigravityCLIFindGlob.FindStringSubmatch(target.String())
			if match == nil {
				return nil, false
			}
			pattern := antigravityCLICaseFoldClass.ReplaceAllString(match[2], "${1}")
			if antigravityCLINameGlob(pattern) != match[2] {
				return nil, false
			}
			directory := match[1]
			if directory == "" {
				directory = "."
				if strings.HasPrefix(target.String(), "/") {
					directory = "/"
				}
			}
			return []antigravityCLIField{
				{"SearchDirectory", antigravityCLIJSONString(directory)},
				{"Pattern", antigravityCLIJSONString(pattern)},
			}, true
		},
	},
	{
		hostName:    "write",
		nativeName:  "write_file",
		description: "Write complete file contents. Requires explicit Overwrite=true and does not append. Existing OMP write permissions still apply.",
		properties: []antigravityCLIField{
			{"Path", antigravityCLIPathSchema},
			{"Content", `{"type":"STRING"}`},
			{"Overwrite", `{"type":"BOOLEAN","description":"Must be true to explicitly allow overwriting."}`},
		},
		required:     []string{"Path", "Content", "Overwrite"},
		hostRequired: []string{"path", "content"},
		decode: func(args ojson.Result) ([]antigravityCLIField, bool) {
			target, content := args.Get("Path"), args.Get("Content")
			if !antigravityCLIOnlyKeys(args, "Path", "Content", "Overwrite") || !antigravityCLIValidRelativePath(target) ||
				content.Type != ojson.String || args.Get("Overwrite").Type != ojson.True {
				return nil, false
			}
			return []antigravityCLIField{
				{"path", antigravityCLIJSONString(target.String())},
				{"content", antigravityCLIJSONString(content.String())},
			}, true
		},
		encode: func(args ojson.Result) ([]antigravityCLIField, bool) {
			target, content := args.Get("path"), args.Get("content")
			if !antigravityCLIOnlyKeys(args, "path", "content") || target.Type != ojson.String || content.Type != ojson.String {
				return nil, false
			}
			return []antigravityCLIField{
				{"Path", antigravityCLIJSONString(target.String())},
				{"Content", antigravityCLIJSONString(content.String())},
				{"Overwrite", "true"},
			}, true
		},
	},
}

// antigravityCLIOnlyKeys reports whether args is an object whose members are all
// among keys, each at most once; the intent field "i" is always allowed. A repeated
// member is rejected because JSON parsers disagree on which value wins.
func antigravityCLIOnlyKeys(args ojson.Result, keys ...string) bool {
	if !args.IsObject() {
		return false
	}
	seen := make(map[string]struct{}, len(keys)+1)
	valid := true
	args.ForEach(func(key, _ ojson.Result) bool {
		name := key.String()
		_, repeated := seen[name]
		seen[name] = struct{}{}
		valid = !repeated && name == "i"
		for _, allowed := range keys {
			valid = valid || (!repeated && name == allowed)
		}
		return valid
	})
	return valid
}

func antigravityCLIValidRelativePath(value ojson.Result) bool {
	return value.Type == ojson.String && antigravityCLIIsRelativePath(value.String())
}

func antigravityCLIValidSearchPath(value ojson.Result) bool {
	if value.Type != ojson.String {
		return false
	}
	target := value.String()
	return target == "." || target == "/" || antigravityCLIIsRelativePath(strings.TrimPrefix(target, "/"))
}

func antigravityCLIIsRelativePath(target string) bool {
	if !antigravityCLIRelativePath.MatchString(target) {
		return false
	}
	for _, part := range strings.Split(target, "/") {
		if part == "" || part != strings.TrimSpace(part) || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// antigravityCLIIsBlank reports whether ECMAScript trim() leaves nothing: its
// whitespace set has U+FEFF and lacks U+0085, unlike unicode.IsSpace.
func antigravityCLIIsBlank(text string) bool {
	return strings.TrimFunc(text, func(char rune) bool {
		return char == '\uFEFF' || (char != '\u0085' && unicode.IsSpace(char))
	}) == ""
}

// antigravityCLIMaxLine is the largest line number the CLI schema declares: the
// largest integer JSON numbers carry exactly.
const antigravityCLIMaxLine = 1<<53 - 1

// antigravityCLIOptionalLine reads an optional one-based line number.
func antigravityCLIOptionalLine(value ojson.Result) (line int64, present, valid bool) {
	if !value.Exists() {
		return 0, false, true
	}
	number := value.Float()
	if value.Type != ojson.Number || number < 1 || number != math.Trunc(number) || number > antigravityCLIMaxLine {
		return 0, true, false
	}
	return int64(number), true, true
}

func antigravityCLIIsBool(value ojson.Result) bool {
	return value.Type == ojson.True || value.Type == ojson.False
}

func antigravityCLILiteralRegex(query string) string {
	return antigravityCLIRegexMeta.ReplaceAllString(query, `\${0}`)
}

// antigravityCLINameGlob gives an all-lowercase pattern case-insensitive matching.
func antigravityCLINameGlob(pattern string) string {
	if strings.ContainsFunc(pattern, func(char rune) bool { return char >= 'A' && char <= 'Z' }) {
		return pattern
	}
	var glob strings.Builder
	for _, char := range pattern {
		if char >= 'a' && char <= 'z' {
			glob.WriteByte('[')
			glob.WriteRune(char)
			glob.WriteRune(char - 'a' + 'A')
			glob.WriteByte(']')
			continue
		}
		glob.WriteRune(char)
	}
	return glob.String()
}
