// Package refute compiles refuted assumptions into failing tests (m3).
//
// When a reviewer marks an assumption 证伪 in the review UI, Preledger writes
// a test into the repo asserting the assumption's premise: a premise that is
// genuinely false fails the suite until the code is fixed, and a fix that
// makes the premise true turns it green. A go.mod repo gets a Go test, a
// package.json repo gets a vitest test; a premise without a mechanical check
// compiles into a TODO stub (t.Skip / it.todo) carrying the premise as a
// comment.
//
// The premise grammar the compiler recognizes — capture and internal/extract
// must emit statements in these forms for them to compile into real
// assertions:
//
//	<path> 存在 | <path> 已存在
//	<path> 存在且含 <key> | <path> 已存在且含 <key>  (含 = literal substring)
//	<file> 的现有内容中待替换片段存在且唯一：<snippet>  (Edit premise)
//	<file> 的现有内容包含待替换片段：<snippet>          (Edit + replace_all)
//
// Snippet checks assert the verbatim Basis (the edit-time old text); the
// snippet shown inside a statement is whitespace-collapsed, possibly
// truncated, and for display only.
package refute

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/SuperMarioYL/preledger/internal/ledger"
)

// Kind classifies a mechanically checkable premise.
type Kind string

const (
	// KindPathExists asserts the referenced path exists in the repo.
	KindPathExists Kind = "path_exists"
	// KindFileHasKey asserts the file exists and contains a key token.
	KindFileHasKey Kind = "file_has_key"
	// KindSnippetOnce asserts a snippet appears in the file exactly once —
	// the premise every non-replace_all Edit makes.
	KindSnippetOnce Kind = "snippet_once"
	// KindSnippetAny asserts a snippet appears at least once — the premise a
	// replace_all Edit makes.
	KindSnippetAny Kind = "snippet_any"
)

// RepoKind classifies the repo a refutation test is compiled into.
type RepoKind string

const (
	RepoGo      RepoKind = "go"
	RepoNode    RepoKind = "node"
	RepoUnknown RepoKind = ""
)

// Premise is a statement parsed into its mechanically checkable parts.
type Premise struct {
	Kind  Kind
	Path  string // repo-relative file the premise is about
	Token string // key or snippet to look for; empty for KindPathExists
}

// Statement markers. Capture and internal/extract must emit premises in
// exactly these forms; everything else compiles into a TODO stub.
const (
	markSnippetOnce = "的现有内容中待替换片段存在且唯一："
	markSnippetAny  = "的现有内容包含待替换片段："
	markHasKey      = "存在且含"
	markExists      = "存在"
)

// ParsePremise parses a ledger statement (with its verbatim basis) into a
// checkable premise. ok is false for statements without a mechanical check —
// they compile into a TODO stub instead of a real assertion. The review UI
// can use this to flag which refutations will produce a runnable red test.
func ParsePremise(statement, basis string) (Premise, bool) {
	s := strings.TrimSpace(statement)
	if path, rest, ok := cut(s, markSnippetOnce); ok {
		p := Premise{Kind: KindSnippetOnce, Path: path, Token: snippetToken(basis, rest)}
		return p, p.Path != "" && p.Token != ""
	}
	if path, rest, ok := cut(s, markSnippetAny); ok {
		p := Premise{Kind: KindSnippetAny, Path: path, Token: snippetToken(basis, rest)}
		return p, p.Path != "" && p.Token != ""
	}
	if path, rest, ok := cut(s, markHasKey); ok {
		p := Premise{Kind: KindFileHasKey, Path: trimAlready(path), Token: rest}
		return p, p.Path != "" && p.Token != ""
	}
	if strings.HasSuffix(s, markExists) {
		p := Premise{Kind: KindPathExists, Path: trimAlready(strings.TrimSuffix(s, markExists))}
		return p, p.Path != ""
	}
	return Premise{}, false
}

// cut splits s at the first occurrence of mark, trimming whitespace around
// both halves.
func cut(s, mark string) (left, right string, ok bool) {
	i := strings.Index(s, mark)
	if i < 0 {
		return "", "", false
	}
	return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+len(mark):]), true
}

// snippetToken prefers the verbatim basis over the display snippet.
func snippetToken(basis, fallback string) string {
	if strings.TrimSpace(basis) != "" {
		return basis
	}
	return fallback
}

// trimAlready drops the 「已」 of 「已存在」 so both phrasings parse the same.
func trimAlready(path string) string {
	path = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(path), "已"))
	return path
}

// DetectRepo classifies the repo for test generation. When both markers
// exist, the plan's go.mod rule wins and the repo counts as Go.
func DetectRepo(repoRoot string) RepoKind {
	if fileExists(filepath.Join(repoRoot, "go.mod")) {
		return RepoGo
	}
	if fileExists(filepath.Join(repoRoot, "package.json")) {
		return RepoNode
	}
	return RepoUnknown
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// Compile renders the refuted assumption into a test file inside repoRoot and
// returns the refutation record for the ledger. TestPath is repo-relative.
// The generated test asserts the premise, so a genuinely false premise fails
// the suite until the code is fixed; re-refuting the same assumption
// overwrites its file. Refutation.LastResult stays empty until a runner
// records an outcome.
func Compile(a ledger.Assumption, repoRoot string) (ledger.Refutation, error) {
	if a.ID == "" {
		return ledger.Refutation{}, errors.New("assumption id is empty")
	}
	id := sanitizeID(a.ID)
	premise, checkable := ParsePremise(a.Statement, a.Basis)
	switch DetectRepo(repoRoot) {
	case RepoGo:
		return compileGo(a, id, premise, checkable, repoRoot)
	case RepoNode:
		return compileNode(a, id, premise, checkable, repoRoot)
	default:
		return ledger.Refutation{}, fmt.Errorf("repo %s 缺少 go.mod 或 package.json：证伪编译目前支持 Go 与 Node 仓库", repoRoot)
	}
}

// compileGo writes the refutation as a Go test in the module root, joining
// the root package when one exists so "go test ./..." picks it up.
func compileGo(a ledger.Assumption, id string, p Premise, checkable bool, root string) (ledger.Refutation, error) {
	pkg := goPackageOf(root)
	if pkg == "" {
		// Root holds no non-test .go files; a regular package with only this
		// test file is still picked up by "go test ./...".
		pkg = "preledger"
	}
	name := "TestPreledgerAssumption_" + id
	src := renderGoTest(pkg, name, a, p, checkable)
	formatted, err := format.Source([]byte(src))
	if err != nil {
		return ledger.Refutation{}, fmt.Errorf("生成的测试未通过语法检查: %w", err)
	}
	file := "preledger_refute_" + id + "_test.go"
	if err := os.WriteFile(filepath.Join(root, file), formatted, 0o644); err != nil {
		return ledger.Refutation{}, fmt.Errorf("write refutation test: %w", err)
	}
	return ledger.Refutation{TestPath: file, TestName: name}, nil
}

// renderGoTest renders the test source. Every dynamic value reaches the
// output as a quoted literal or a %s argument, so premise text containing
// '%' or quotes cannot corrupt the generated code.
func renderGoTest(pkg, name string, a ledger.Assumption, p Premise, checkable bool) string {
	var b strings.Builder
	b.WriteString("// Code generated by preledger. 重复证伪同一假设会覆盖本文件。\n//\n")
	fmt.Fprintf(&b, "// 假设：%s\n", oneLine(a.Statement, 0))
	if basis := oneLine(a.Basis, 160); basis != "" {
		fmt.Fprintf(&b, "// 依据（转录原文）：%s\n", basis)
	}
	if checkable {
		b.WriteString("//\n// 评审者已将该假设标记为证伪。本测试断言前提成立：\n")
		b.WriteString("// 前提确实为假时套件变红，修复代码使其成立后转绿。\n")
	} else {
		b.WriteString("//\n// TODO: 该前提无法机械化验证，请人工复核后补充断言。\n")
	}
	fmt.Fprintf(&b, "\npackage %s\n\nimport (\n", pkg)
	for _, imp := range goImports(p, checkable) {
		fmt.Fprintf(&b, "\t%q\n", imp)
	}
	b.WriteString(")\n\n")
	fmt.Fprintf(&b, "func %s(t *testing.T) {\n", name)

	if !checkable {
		b.WriteString("\t// 前提留待人工断言；置为跳过，避免套件出现无法归因的红。\n")
		fmt.Fprintf(&b, "\tt.Skip(%s)\n}\n", goString("preledger：该前提无法机械化验证（见文件头 TODO），已按评审结论标记为证伪"))
		return b.String()
	}

	id, stmt, path, token := goString(a.ID), goString(a.Statement), goString(p.Path), goString(p.Token)
	if p.Kind != KindPathExists {
		fmt.Fprintf(&b, "\tdata, err := os.ReadFile(%s)\n", path)
		b.WriteString("\tif err != nil {\n")
		b.WriteString("\t\tt.Fatalf(\"preledger 假设 %s：前提「%s」未成立：%s 应存在：%v\", " + id + ", " + stmt + ", " + path + ", err)\n")
		b.WriteString("\t}\n")
	}
	switch p.Kind {
	case KindPathExists:
		b.WriteString("\tif _, err := os.Stat(" + path + "); err != nil {\n")
		b.WriteString("\t\tt.Fatalf(\"preledger 假设 %s：前提「%s」未成立：%s 应存在：%v\", " + id + ", " + stmt + ", " + path + ", err)\n")
		b.WriteString("\t}\n")
	case KindFileHasKey:
		b.WriteString("\tif !strings.Contains(string(data), " + token + ") {\n")
		b.WriteString("\t\tt.Fatalf(\"preledger 假设 %s：前提「%s」未成立：%s 中未找到 %s\", " + id + ", " + stmt + ", " + path + ", " + token + ")\n")
		b.WriteString("\t}\n")
	case KindSnippetOnce:
		b.WriteString("\tif n := strings.Count(string(data), " + token + "); n != 1 {\n")
		b.WriteString("\t\tt.Fatalf(\"preledger 假设 %s：前提「%s」未成立：%s 中待替换片段应存在且唯一，实际出现 %d 次\", " + id + ", " + stmt + ", " + path + ", n)\n")
		b.WriteString("\t}\n")
	case KindSnippetAny:
		b.WriteString("\tif !strings.Contains(string(data), " + token + ") {\n")
		b.WriteString("\t\tt.Fatalf(\"preledger 假设 %s：前提「%s」未成立：%s 中未找到待替换片段\", " + id + ", " + stmt + ", " + path + ")\n")
		b.WriteString("\t}\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func goImports(p Premise, checkable bool) []string {
	if !checkable {
		return []string{"testing"}
	}
	imports := []string{"os"}
	if p.Kind != KindPathExists {
		imports = append(imports, "strings")
	}
	return append(imports, "testing")
}

// compileNode writes the refutation as a vitest test in the repo root, where
// vitest's default include pattern picks it up.
func compileNode(a ledger.Assumption, id string, p Premise, checkable bool, root string) (ledger.Refutation, error) {
	file := "preledger_refute_" + id + ".test.js"
	src := renderNodeTest(a, p, checkable)
	if err := os.WriteFile(filepath.Join(root, file), []byte(src), 0o644); err != nil {
		return ledger.Refutation{}, fmt.Errorf("write refutation test: %w", err)
	}
	return ledger.Refutation{TestPath: file, TestName: "preledger assumption " + id}, nil
}

func renderNodeTest(a ledger.Assumption, p Premise, checkable bool) string {
	var b strings.Builder
	b.WriteString("// Code generated by preledger. 重复证伪同一假设会覆盖本文件。\n//\n")
	fmt.Fprintf(&b, "// 假设：%s\n", oneLine(a.Statement, 0))
	if basis := oneLine(a.Basis, 160); basis != "" {
		fmt.Fprintf(&b, "// 依据（转录原文）：%s\n", basis)
	}
	if checkable {
		b.WriteString("//\n// 评审者已将该假设标记为证伪。本测试断言前提成立：\n")
		b.WriteString("// 前提确实为假时 vitest 变红，修复代码使其成立后转绿。\n")
	} else {
		b.WriteString("//\n// TODO: 该前提无法机械化验证，请人工复核后补充断言。\n")
	}
	b.WriteString("\n")

	if !checkable {
		b.WriteString("import { describe, it } from \"vitest\";\n\n")
		fmt.Fprintf(&b, "describe(%s, () => {\n", jsString("preledger assumption "+a.ID))
		fmt.Fprintf(&b, "  it.todo(%s);\n", jsString(oneLine(a.Statement, 0)))
		b.WriteString("});\n")
		return b.String()
	}

	switch p.Kind {
	case KindPathExists:
		b.WriteString("import { existsSync } from \"node:fs\";\n")
	case KindFileHasKey:
		b.WriteString("import { existsSync, readFileSync } from \"node:fs\";\n")
	default:
		b.WriteString("import { readFileSync } from \"node:fs\";\n")
	}
	b.WriteString("import { describe, expect, it } from \"vitest\";\n\n")

	fmt.Fprintf(&b, "describe(%s, () => {\n", jsString("preledger assumption "+a.ID))
	fmt.Fprintf(&b, "  it(%s, () => {\n", jsString(oneLine(a.Statement, 0)))
	path, token := jsString(p.Path), jsString(p.Token)
	missing := jsString(fmt.Sprintf("preledger 假设 %s：前提「%s」未成立：%s 应存在", a.ID, a.Statement, p.Path))
	switch p.Kind {
	case KindPathExists:
		fmt.Fprintf(&b, "    expect(existsSync(%s), %s).toBe(true);\n", path, missing)
	case KindFileHasKey:
		fmt.Fprintf(&b, "    expect(existsSync(%s), %s).toBe(true);\n", path, missing)
		fmt.Fprintf(&b, "    expect(readFileSync(%s, \"utf8\")).toContain(%s);\n", path, token)
	case KindSnippetOnce:
		fmt.Fprintf(&b, "    const data = readFileSync(%s, \"utf8\");\n", path)
		fmt.Fprintf(&b, "    expect(data.split(%s).length - 1, %s).toBe(1);\n", token,
			jsString(fmt.Sprintf("preledger 假设 %s：前提「%s」未成立：%s 中待替换片段应存在且唯一", a.ID, a.Statement, p.Path)))
	case KindSnippetAny:
		fmt.Fprintf(&b, "    expect(readFileSync(%s, \"utf8\")).toContain(%s);\n", path, token)
	}
	b.WriteString("  });\n});\n")
	return b.String()
}

// Refute is the entry point the review UI's 证伪 handler calls: it resolves
// the assumption to refuted in the ledger and compiles its test into the
// repo. 确认 needs no compilation and uses ledger.Store.SetStatus directly.
func Refute(store *ledger.Store, id, repoRoot string) (ledger.Assumption, error) {
	current, err := store.Current()
	if err != nil {
		return ledger.Assumption{}, fmt.Errorf("read ledger: %w", err)
	}
	var target ledger.Assumption
	found := false
	for _, a := range current {
		if a.ID == id {
			target, found = a, true
			break
		}
	}
	if !found {
		return ledger.Assumption{}, fmt.Errorf("assumption %s not found in ledger", id)
	}
	refutation, err := Compile(target, repoRoot)
	if err != nil {
		return ledger.Assumption{}, err
	}
	target.Status = ledger.StatusRefuted
	target.Refutation = &refutation
	if err := store.Append(target); err != nil {
		return ledger.Assumption{}, fmt.Errorf("append refutation record: %w", err)
	}
	return target, nil
}

// goPackageOf returns the package clause shared by the non-test .go files
// directly inside dir, or "" when the directory holds none.
func goPackageOf(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "//") {
				continue
			}
			if pkg, ok := strings.CutPrefix(line, "package "); ok {
				return strings.TrimSpace(pkg)
			}
			break
		}
	}
	return ""
}

// sanitizeID keeps the assumption id usable in file names and Go identifiers;
// ledger ids are 16 hex chars, anything else degrades to underscores.
func sanitizeID(id string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			return r
		default:
			return '_'
		}
	}, id)
}

// oneLine collapses s to a single line for comments and test titles; a
// positive limit truncates by runes. Verbatim text stays in the ledger.
func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if limit > 0 {
		if r := []rune(s); len(r) > limit {
			return string(r[:limit]) + "…"
		}
	}
	return s
}

// goString renders s as a Go string literal.
func goString(s string) string { return strconv.Quote(s) }

// jsString renders s as a JavaScript string literal via JSON escaping.
func jsString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}
