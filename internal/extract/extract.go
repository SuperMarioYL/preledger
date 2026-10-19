// Package extract derives load-bearing assumption entries from one edit.
//
// hook.Capture records the mechanical premise every edit provably makes from
// its own payload. This package layers the plan's heuristics on top, each one
// looking for a premise the edit makes about the repo that the tree does not
// already answer:
//
//   - a new dependency: the edited text imports a module the manifest does
//     not declare → "go.mod 存在且含 <module>" / "package.json 存在且含 <pkg>"
//   - a config or env key not found in the tree: the edited text reads a
//     config file that is absent → "<path> 存在" (plus "<path> 存在且含 <key>"
//     for dotted keys), or uses an undeclared env var → ".env 存在且含 <KEY>"
//   - a path absent from the repo: a Write lands on a file git reports as new
//     → "<path> 存在"
//
// Statements are emitted in exactly the grammar internal/refute parses, so a
// refuted heuristic premise compiles into a real failing test instead of a
// TODO stub. Capture is heuristic-first: everything here works offline from
// the edit payload and a bounded repo scan. Triage (optional) additionally
// asks a DeepSeek/GLM OpenAI-compatible endpoint to drop non-load-bearing
// candidates; without a provider or on any error the heuristics stand alone.
package extract

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/SuperMarioYL/preledger/internal/config"
	"github.com/SuperMarioYL/preledger/internal/ledger"
)

// maxEntriesPerEdit caps how many heuristic entries one edit may add, so a
// pathological diff cannot flood the ledger with noise.
const maxEntriesPerEdit = 8

// Bounded repo-scan limits; the scan is a noise filter, not an indexer.
const (
	maxScanFiles     = 4000
	maxScanFileBytes = 512 << 10
)

// skippedScanDirs are never searched for keys or paths.
var skippedScanDirs = map[string]bool{
	".git": true, ".preledger": true, "node_modules": true, "vendor": true, "dist": true,
}

// Edit carries everything the heuristics need beyond hook.Capture's
// mechanical premise: the edit ref for the ledger, the touched file, and the
// incoming text (new_string for Edit, full content for Write).
type Edit struct {
	Ref     ledger.EditRef
	Tool    string // hook.ToolEdit or hook.ToolWrite
	File    string // repo-relative path of the edited file
	NewText string
}

// quotes are assistant texts from the transcript preceding the edit; they may
// be empty (offline capture), in which case the triggering code line is the
// basis quote instead.
type quotes = []string

// FromEdit runs the heuristics over one edit and returns the extra ledger
// entries it implies, in a stable order. It never touches the ledger itself.
func FromEdit(e Edit, repoRoot string, agentSaid quotes) []ledger.Assumption {
	var out []ledger.Assumption
	add := func(statement, basis string) {
		if len(out) >= maxEntriesPerEdit || statement == "" {
			return
		}
		for _, prev := range out {
			if prev.Statement == statement {
				return
			}
		}
		out = append(out, ledger.Assumption{
			ID:        ledger.NewID(e.Ref, statement),
			EditRef:   e.Ref,
			Statement: statement,
			Basis:     basis,
			Status:    ledger.StatusOpen,
		})
	}

	missingDependencies(e, repoRoot, add)
	missingConfigKeys(e, repoRoot, agentSaid, add)
	newPathPremise(e, repoRoot, agentSaid, add)
	return out
}

// basisFor picks the verbatim quote for a premise: the agent's own line that
// mentions the token, falling back to the triggering code line from the edit.
func basisFor(text string, agentSaid quotes, tokens ...string) string {
	for _, quote := range agentSaid {
		if line := mentionLine(quote, tokens); line != "" {
			return line
		}
	}
	return mentionLine(text, tokens)
}

// mentionLine returns the first line of text containing every non-empty
// token, collapsed to one line and truncated for display; verbatim text
// stays in the ledger.
func mentionLine(text string, tokens []string) string {
	var active []string
	for _, t := range tokens {
		if t != "" {
			active = append(active, t)
		}
	}
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		ok := true
		for _, t := range active {
			if !strings.Contains(line, t) {
				ok = false
				break
			}
		}
		if ok {
			return truncate(line, 160)
		}
	}
	return ""
}

func truncate(s string, limit int) string {
	if r := []rune(s); len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return s
}

// --- heuristic 1: a new dependency not in the manifest ---

// goImportRe matches one Go import path inside an import declaration line.
var goImportRe = regexp.MustCompile(`^\s*(?:[_\p{L}\p{Nl}]\w*\s+)?"([^"]+)"`)

// jsSpecifierRe matches bare module specifiers in JS/TS import and require
// forms; relative paths, absolute paths and node:/data: builtins are skipped
// by the caller.
var jsSpecifierRe = regexp.MustCompile(`(?:\bfrom\s*|\brequire\s*\(\s*|\bimport\s+)["']([^"']+)["']`)

func missingDependencies(e Edit, repoRoot string, add func(statement, basis string)) {
	switch e.Tool {
	case "Edit", "Write":
	default:
		return
	}
	if e.NewText == "" {
		return
	}
	manifest := detectManifest(repoRoot)
	if manifest == "go.mod" {
		for _, path := range goImports(e.NewText) {
			module := goModuleToken(path)
			if goDepPresent(repoRoot, path, module) {
				continue
			}
			add(
				fmt.Sprintf("go.mod 存在且含 %s", module),
				basisFor(e.NewText, nil, path),
			)
		}
		return
	}
	if manifest == "package.json" {
		for _, pkg := range jsBareImports(e.NewText) {
			if nodeDepPresent(repoRoot, pkg) {
				continue
			}
			add(
				fmt.Sprintf("package.json 存在且含 %s", pkg),
				basisFor(e.NewText, nil, pkg),
			)
		}
	}
}

// detectManifest classifies the repo by its manifest, mirroring the plan's
// rule that go.mod wins over package.json.
func detectManifest(repoRoot string) string {
	if fileExists(filepath.Join(repoRoot, "go.mod")) {
		return "go.mod"
	}
	if fileExists(filepath.Join(repoRoot, "package.json")) {
		return "package.json"
	}
	return ""
}

// goImports extracts non-stdlib import paths from Go source text: paths
// inside an import block plus single-line import statements. Standard
// library paths have no dot in the first segment and are skipped.
func goImports(text string) []string {
	var out []string
	seen := map[string]bool{}
	inBlock := false
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case regexp.MustCompile(`^\s*import\s*\(`).MatchString(line):
			inBlock = true
			continue
		case inBlock && regexp.MustCompile(`^\s*\)`).MatchString(line):
			inBlock = false
			continue
		}
		if !inBlock {
			if m := regexp.MustCompile(`^\s*import\s+(?:[_\p{L}\p{Nl}]\w*\s+)?"([^"]+)"`).FindStringSubmatch(line); m != nil {
				addImport(m[1], &out, seen)
			}
			continue
		}
		if m := goImportRe.FindStringSubmatch(line); m != nil {
			addImport(m[1], &out, seen)
		}
	}
	return out
}

func addImport(path string, out *[]string, seen map[string]bool) {
	if seen[path] || !strings.Contains(strings.SplitN(path, "/", 2)[0], ".") {
		return // stdlib or duplicate
	}
	seen[path] = true
	*out = append(*out, path)
}

// goModuleToken guesses the module path an import path resolves to: the plan
// only needs a token that lands in go.mod once the dependency is added. Hosts
// with owner/repo layouts need three segments, golang.org/x paths three too.
func goModuleToken(importPath string) string {
	parts := strings.Split(importPath, "/")
	need := 2
	switch {
	case parts[0] == "github.com" || parts[0] == "gitlab.com" || parts[0] == "bitbucket.org":
		need = 3
	case parts[0] == "golang.org" && len(parts) > 1 && parts[1] == "x":
		need = 3
	}
	if need > len(parts) {
		need = len(parts)
	}
	return strings.Join(parts[:need], "/")
}

// goDepPresent reports whether go.mod already covers the import path, either
// by containing the module token verbatim or by requiring a module that is a
// prefix of the path.
func goDepPresent(repoRoot, importPath, token string) bool {
	data, err := os.ReadFile(filepath.Join(repoRoot, "go.mod"))
	if err != nil {
		return false
	}
	if strings.Contains(string(data), token) {
		return true
	}
	for _, module := range goRequiredModules(string(data)) {
		if importPath == module || strings.HasPrefix(importPath, module+"/") {
			return true
		}
	}
	return false
}

// goRequiredModules parses go.mod require entries (single and block forms).
func goRequiredModules(goMod string) []string {
	var mods []string
	inBlock := false
	for _, line := range strings.Split(goMod, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		switch {
		case fields[0] == "require" && len(fields) >= 3 && fields[1] == "(":
			inBlock = true
		case fields[0] == "require" && len(fields) >= 3:
			mods = append(mods, fields[1])
		case fields[0] == ")":
			inBlock = false
		case inBlock && len(fields) >= 2:
			mods = append(mods, fields[0])
		}
	}
	return mods
}

// jsBareImports extracts bare package specifiers from JS/TS text. Scoped
// packages collapse to "@scope/pkg"; subpaths collapse to their package.
func jsBareImports(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range jsSpecifierRe.FindAllStringSubmatch(text, -1) {
		spec := m[1]
		if seen[spec] || spec == "" ||
			strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") ||
			strings.HasPrefix(spec, "node:") || strings.HasPrefix(spec, "data:") {
			continue
		}
		parts := strings.Split(spec, "/")
		if strings.HasPrefix(spec, "@") {
			if len(parts) < 2 {
				continue // malformed scope specifier
			}
			spec = parts[0] + "/" + parts[1]
		} else {
			spec = parts[0]
		}
		if !seen[spec] {
			seen[spec] = true
			out = append(out, spec)
		}
	}
	return out
}

// nodeDepPresent reports whether package.json declares the package.
func nodeDepPresent(repoRoot, pkg string) bool {
	data, err := os.ReadFile(filepath.Join(repoRoot, "package.json"))
	if err != nil {
		return false
	}
	var manifest struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		return false
	}
	if _, ok := manifest.Dependencies[pkg]; ok {
		return true
	}
	_, ok := manifest.DevDependencies[pkg]
	return ok
}

// --- heuristic 2: a config or env key not found in the tree ---

// configRefRe matches file references a program might read at runtime.
var configRefRe = regexp.MustCompile(`[\w.-]+\.(?:ya?ml|toml|json|ini|conf)\b`)

// dottedKeyRe matches quoted dotted keys such as "server.port". Import-style
// paths cannot match (no slash in the class) and file extensions are filtered
// by the caller.
var dottedKeyRe = regexp.MustCompile(`"([A-Za-z][A-Za-z0-9_]*(?:\.[A-Za-z0-9_]+)+)"`)

// envGoRe / envJSRe match environment variable reads.
var (
	envGoRe = regexp.MustCompile(`os\.(?:Getenv|LookupEnv)\("([A-Z][A-Z0-9_]+)"\)`)
	envJSRe = regexp.MustCompile(`process\.env(?:\.|\[")([A-Z][A-Z0-9_]+)`)
)

// fileLikeEndings keeps dotted keys from colliding with file names such as
// "config.yaml".
var fileLikeEndings = map[string]bool{
	"yaml": true, "yml": true, "toml": true, "json": true, "ini": true,
	"conf": true, "txt": true, "md": true, "go": true, "js": true, "ts": true,
	"html": true, "css": true, "env": true, "mod": true, "sum": true,
}

func missingConfigKeys(e Edit, repoRoot string, agentSaid quotes, add func(statement, basis string)) {
	if e.NewText == "" {
		return
	}
	refs := uniqueMatches(configRefRe, e.NewText)
	keys := dottedKeys(e.NewText)
	envKeys := uniqueMatches(envGoRe, e.NewText)
	for _, m := range uniqueMatches(envJSRe, e.NewText) {
		envKeys = append(envKeys, m)
	}
	envKeys = uniqueStrings(envKeys)

	for _, ref := range refs {
		if ref == e.File || pathExists(repoRoot, ref) {
			continue // the tree already answers this premise
		}
		emitted := 0
		for _, key := range keys {
			if lastSegmentIsFileLike(key) || key == ref {
				continue
			}
			if repoContains(repoRoot, e.File, key) {
				continue // declared somewhere else in the tree
			}
			add(
				fmt.Sprintf("%s 存在且含 %s", ref, key),
				basisFor(e.NewText, agentSaid, key),
			)
			emitted++
			if emitted >= 3 {
				break
			}
		}
		if emitted == 0 {
			add(fmt.Sprintf("%s 存在", ref), basisFor(e.NewText, agentSaid, ref))
		}
	}

	for _, key := range envKeys {
		if repoContains(repoRoot, e.File, key) {
			continue
		}
		add(
			fmt.Sprintf(".env 存在且含 %s", key),
			basisFor(e.NewText, agentSaid, key),
		)
	}
}

// dottedKeys returns quoted dotted keys in text; file-name-like keys such as
// "config.yaml" are filtered by lastSegmentIsFileLike at the call site.
func dottedKeys(text string) []string {
	return uniqueMatches(dottedKeyRe, text)
}

func lastSegmentIsFileLike(key string) bool {
	segments := strings.Split(key, ".")
	return fileLikeEndings[strings.ToLower(segments[len(segments)-1])]
}

func uniqueMatches(re *regexp.Regexp, text string) []string {
	var out []string
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		if len(m) > 1 {
			if m[1] != "" {
				out = append(out, m[1])
			}
			continue
		}
		if m[0] != "" {
			out = append(out, m[0])
		}
	}
	return uniqueStrings(out)
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// --- heuristic 3: a Write landing on a path absent from the repo ---

// newPathPremise emits "<path> 存在" when git reports the written file as new
// (untracked or staged-add). Without git the premise is not observable at
// capture time, so it is skipped rather than guessed.
func newPathPremise(e Edit, repoRoot string, agentSaid quotes, add func(statement, basis string)) {
	if e.Tool != "Write" || e.File == "" {
		return
	}
	cmd := exec.Command("git", "-C", repoRoot, "status", "--porcelain", "--", e.File)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return // not a repo, git missing, or the path is untracked-unknown
	}
	line := strings.TrimRight(stdout.String(), "\n")
	if line == "" {
		return
	}
	status := strings.Fields(line)[0]
	if status != "??" && !strings.HasPrefix(status, "A") {
		return // an already-tracked file, not a new path
	}
	add(
		fmt.Sprintf("%s 存在", e.File),
		truncate(line, 160), // the porcelain line is the verbatim evidence
	)
}

// --- repo scan ---

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func pathExists(root, rel string) bool {
	rel = filepath.FromSlash(rel)
	if filepath.IsAbs(rel) {
		return false // machine-specific paths are not portable premises
	}
	_, err := os.Stat(filepath.Join(root, rel))
	return err == nil
}

// repoContains reports whether needle appears in any file of the repo other
// than skipFile, within the bounded scan limits.
func repoContains(root, skipFile, needle string) bool {
	if needle == "" {
		return false
	}
	found := false
	count := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries simply do not match
		}
		if d.IsDir() {
			if skippedScanDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if found || count > maxScanFiles {
			return fs.SkipAll
		}
		count++
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil || rel == skipFile {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil || info.Size() > maxScanFileBytes {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if bytes.Contains(data, []byte(needle)) {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

// --- optional LLM triage (DeepSeek / GLM, OpenAI-compatible) ---

// Triage asks the configured provider to mark which candidates are
// load-bearing, and drops the rest. It is strictly optional: without a
// provider and key the candidates pass through untouched, and any failure —
// network, schema, rate limit — returns the full candidate set so capture
// degrades to the offline heuristics instead of losing assumptions.
func Triage(ctx context.Context, cfg config.Triage, candidates []ledger.Assumption) ([]ledger.Assumption, error) {
	if cfg.Provider == "" || cfg.APIKey == "" || len(candidates) == 0 {
		return candidates, nil
	}
	var b strings.Builder
	b.WriteString("以下是某次代码编辑被记录的候选假设（前提）。判断每条是否是承重假设（若不成立会导致缺陷或构建失败）。\n只输出 JSON 数组，格式：[{\"index\":0,\"load_bearing\":true},...]，不要输出其他文字。\n\n")
	for i, a := range candidates {
		fmt.Fprintf(&b, "%d. %s\n", i, a.Statement)
	}

	payload, err := json.Marshal(map[string]any{
		"model": cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": "你是严谨的代码评审助手，只输出 JSON。"},
			{"role": "user", "content": b.String()},
		},
		"temperature": 0,
	})
	if err != nil {
		return candidates, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST",
		strings.TrimSuffix(cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return candidates, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return candidates, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return candidates, err
	}
	if resp.StatusCode != http.StatusOK {
		return candidates, fmt.Errorf("triage %s: HTTP %d", cfg.Provider, resp.StatusCode)
	}
	var completion struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &completion) != nil || len(completion.Choices) == 0 {
		return candidates, errors.New("triage: unexpected completion shape")
	}

	content := completion.Choices[0].Message.Content
	start, end := strings.Index(content, "["), strings.LastIndex(content, "]")
	if start < 0 || end <= start {
		return candidates, errors.New("triage: no JSON array in completion")
	}
	var marks []struct {
		Index       int  `json:"index"`
		LoadBearing bool `json:"load_bearing"`
	}
	if json.Unmarshal([]byte(content[start:end+1]), &marks) != nil || len(marks) == 0 {
		return candidates, errors.New("triage: unparseable verdict")
	}

	keep := make([]ledger.Assumption, 0, len(candidates))
	for i, a := range candidates {
		if a.Status != ledger.StatusOpen {
			keep = append(keep, a) // never drop records a reviewer already resolved
			continue
		}
		for _, m := range marks {
			if m.Index == i && m.LoadBearing {
				keep = append(keep, a)
				break
			}
		}
	}
	return keep, nil
}
