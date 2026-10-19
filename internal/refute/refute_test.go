package refute

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuperMarioYL/preledger/internal/ledger"
)

func TestParsePremiseGrammar(t *testing.T) {
	cases := []struct {
		name      string
		statement string
		basis     string
		want      Premise
		ok        bool
	}{
		{
			name:      "key",
			statement: "config.yaml 存在且含 server.port",
			want:      Premise{Kind: KindFileHasKey, Path: "config.yaml", Token: "server.port"},
			ok:        true,
		},
		{
			name:      "already-key phrasing",
			statement: "settings.json 已存在且含 server.port",
			want:      Premise{Kind: KindFileHasKey, Path: "settings.json", Token: "server.port"},
			ok:        true,
		},
		{
			name:      "path exists",
			statement: "config.yaml 存在",
			want:      Premise{Kind: KindPathExists, Path: "config.yaml"},
			ok:        true,
		},
		{
			name:      "already-exists phrasing",
			statement: "settings.json 已存在",
			want:      Premise{Kind: KindPathExists, Path: "settings.json"},
			ok:        true,
		},
		{
			name:      "snippet once prefers verbatim basis",
			statement: "main.go 的现有内容中待替换片段存在且唯一：if err != nil",
			basis:     "if err != nil {",
			want:      Premise{Kind: KindSnippetOnce, Path: "main.go", Token: "if err != nil {"},
			ok:        true,
		},
		{
			name:      "snippet any prefers verbatim basis",
			statement: "main.go 的现有内容包含待替换片段：foo",
			basis:     "foo := bar()",
			want:      Premise{Kind: KindSnippetAny, Path: "main.go", Token: "foo := bar()"},
			ok:        true,
		},
		{
			name:      "snippet falls back to statement tail",
			statement: "main.go 的现有内容包含待替换片段：foo",
			want:      Premise{Kind: KindSnippetAny, Path: "main.go", Token: "foo"},
			ok:        true,
		},
		{name: "write rewrite is a stub", statement: "main.go 将被整体重写，旧内容全部废弃"},
		{name: "free text is a stub", statement: "server listens on port 7830"},
		{name: "exists without a path", statement: "存在"},
		{name: "key marker without a key", statement: "config.yaml 存在且含"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParsePremise(tc.statement, tc.basis)
			if ok != tc.ok {
				t.Fatalf("ParsePremise(%q) ok = %v, want %v", tc.statement, ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Fatalf("ParsePremise(%q) = %+v, want %+v", tc.statement, got, tc.want)
			}
		})
	}
}

func TestDetectRepoPrefersGo(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module example.local/gadget\n\ngo 1.24\n")
	writeFile(t, repo, "package.json", "{}\n")
	if DetectRepo(repo) != RepoGo {
		t.Fatal("go.mod must win when both repo markers exist")
	}
	if DetectRepo(t.TempDir()) != RepoUnknown {
		t.Fatal("a repo with neither marker must be unknown")
	}
}

// runGoTest runs "go test ./..." in dir; go test output is the m3 evidence:
// a refuted-but-false premise must turn the suite red, a fix must turn it
// green again.
func runGoTest(t *testing.T, dir string) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("go test exited with error (expected for red cases): %v", err)
	}
	return string(out)
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestCompileGoHasKeyRedThenGreen(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the go toolchain")
	}
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module example.local/gadget\n\ngo 1.24\n")
	// 前提实际为假：config.yaml 尚不含 server.port 字面量。
	writeFile(t, repo, "config.yaml", "server:\n  host: localhost\n")

	a := ledger.Assumption{
		ID:        "0123456789abcdef",
		Statement: "config.yaml 存在且含 server.port",
		Basis:     "读取 config.yaml 的 server.port",
		Status:    ledger.StatusOpen,
	}
	ref, err := Compile(a, repo)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if ref.TestPath != "preledger_refute_0123456789abcdef_test.go" || ref.TestName != "TestPreledgerAssumption_0123456789abcdef" {
		t.Fatalf("unexpected refutation record: %+v", ref)
	}
	// 根目录没有其他 .go 文件，测试应声明自己的包并被 go test ./... 收录。
	if src := readRepoFile(t, filepath.Join(repo, ref.TestPath)); !strings.Contains(src, "package preledger") {
		t.Fatalf("test-only root package expected, got:\n%s", src)
	}

	out := runGoTest(t, repo)
	if _, err := os.Stat(filepath.Join(repo, ref.TestPath)); err != nil {
		t.Fatalf("compiled test missing: %v", err)
	}
	if !strings.Contains(out, "--- FAIL: "+ref.TestName) {
		t.Fatalf("refuted-but-false premise must fail the suite, output:\n%s", out)
	}

	// 修复代码使前提成立后转绿。
	writeFile(t, repo, "config.yaml", "server:\n  host: localhost\nserver.port: 8080\n")
	if out := runGoTest(t, repo); strings.Contains(out, "--- FAIL") {
		t.Fatalf("premise now holds, suite must pass, output:\n%s", out)
	}
}

func TestCompileGoSnippetOnceJoinsRootPackage(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the go toolchain")
	}
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module example.local/gadget\n\ngo 1.24\n")
	writeFile(t, repo, "main.go", "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n")

	a := ledger.Assumption{
		ID:        "fedcba9876543210",
		EditRef:   ledger.EditRef{SessionID: "s1", Tool: "Edit", File: "main.go"},
		Statement: "main.go 的现有内容中待替换片段存在且唯一：println(\"hello\")",
		Basis:     "println(\"hello\")",
		Status:    ledger.StatusOpen,
	}
	ref, err := Compile(a, repo)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	src := readRepoFile(t, filepath.Join(repo, ref.TestPath))
	if !strings.Contains(src, "package main") {
		t.Fatalf("test must join the root package, got:\n%s", src)
	}

	// 前提当前成立：测试绿。
	if out := runGoTest(t, repo); strings.Contains(out, "--- FAIL") {
		t.Fatalf("premise holds, suite must pass, output:\n%s", out)
	}

	// 后续编辑破坏前提（片段消失）：测试红。替换文本保持仓库可编译。
	writeFile(t, repo, "main.go", "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n")
	if out := runGoTest(t, repo); !strings.Contains(out, "--- FAIL: "+ref.TestName) {
		t.Fatalf("broken premise must fail the suite, output:\n%s", out)
	}
}

func TestCompileGoStubForUncheckablePremise(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module example.local/gadget\n\ngo 1.24\n")

	a := ledger.Assumption{
		ID:        "aaaaaaaaaaaaaaaa",
		EditRef:   ledger.EditRef{SessionID: "s1", Tool: "Write", File: "main.go"},
		Statement: "main.go 将被整体重写，旧内容全部废弃",
		Basis:     "旧内容全文",
		Status:    ledger.StatusOpen,
	}
	ref, err := Compile(a, repo)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	src := readRepoFile(t, filepath.Join(repo, ref.TestPath))
	if !strings.Contains(src, "TODO") || !strings.Contains(src, a.Statement) {
		t.Fatalf("stub must carry the premise as a TODO comment:\n%s", src)
	}
	if !strings.Contains(src, "t.Skip(") {
		t.Fatalf("stub must skip rather than assert:\n%s", src)
	}
	if strings.Contains(src, `"os"`) {
		t.Fatalf("stub needs no file access imports:\n%s", src)
	}
}

func TestCompileNodeVitestTest(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "package.json", "{\"name\":\"gadget\",\"private\":true}\n")

	a := ledger.Assumption{
		ID:        "1111222233334444",
		Statement: "config.yaml 存在且含 server.port",
		Status:    ledger.StatusOpen,
	}
	ref, err := Compile(a, repo)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if ref.TestPath != "preledger_refute_1111222233334444.test.js" || ref.TestName != "preledger assumption 1111222233334444" {
		t.Fatalf("unexpected refutation record: %+v", ref)
	}
	src := readRepoFile(t, filepath.Join(repo, ref.TestPath))
	for _, want := range []string{
		`from "vitest"`,
		`existsSync("config.yaml")`,
		`toContain("server.port")`,
		a.Statement,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("generated vitest test missing %s:\n%s", want, src)
		}
	}

	// 无法机械化验证的前提生成 it.todo 桩。
	a.ID = "5555666677778888"
	a.Statement = "server 将被整体重写，旧内容全部废弃"
	ref, err = Compile(a, repo)
	if err != nil {
		t.Fatalf("compile stub: %v", err)
	}
	src = readRepoFile(t, filepath.Join(repo, ref.TestPath))
	if !strings.Contains(src, "it.todo(") || !strings.Contains(src, a.Statement) {
		t.Fatalf("vitest stub must carry the premise as a TODO:\n%s", src)
	}
}

func TestRefuteWritesTestAndLedgerRecord(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module example.local/gadget\n\ngo 1.24\n")
	writeFile(t, repo, "config.yaml", "server.port: 8080\n")

	store := ledger.Open(filepath.Join(repo, ".preledger", "ledger.jsonl"))
	a := ledger.Assumption{
		ID:        "0123456789abcdef",
		EditRef:   ledger.EditRef{SessionID: "s1", Tool: "Edit", File: "config.yaml"},
		Statement: "config.yaml 存在且含 server.port",
		Status:    ledger.StatusOpen,
	}
	if err := store.Append(a); err != nil {
		t.Fatalf("append: %v", err)
	}

	got, err := Refute(store, a.ID, repo)
	if err != nil {
		t.Fatalf("refute: %v", err)
	}
	if got.Status != ledger.StatusRefuted || got.Refutation == nil {
		t.Fatalf("refute must persist refuted status and refutation: %+v", got)
	}
	if got.Refutation.TestPath == "" || got.Refutation.TestName == "" {
		t.Fatalf("refutation must name the compiled test: %+v", got.Refutation)
	}
	if _, err := os.Stat(filepath.Join(repo, got.Refutation.TestPath)); err != nil {
		t.Fatalf("compiled test missing: %v", err)
	}

	all, err := store.ReadAll()
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	if len(all) != 2 || all[0].Status != ledger.StatusOpen || all[1].Status != ledger.StatusRefuted || all[1].Refutation == nil {
		t.Fatalf("ledger must keep the open record and append the refutation: %+v", all)
	}

	if _, err := Refute(store, "missing", repo); err == nil {
		t.Fatal("refuting an unknown id must error")
	}
}

func TestCompileRequiresGoOrNodeRepo(t *testing.T) {
	repo := t.TempDir()
	_, err := Compile(ledger.Assumption{ID: "abcd", Statement: "config.yaml 存在"}, repo)
	if err == nil || !strings.Contains(err.Error(), "go.mod") {
		t.Fatalf("unsupported repo must error naming the markers, got %v", err)
	}
	if _, ok := ParsePremise("config.yaml 存在", ""); !ok {
		t.Fatal("the premise itself is checkable; the failure must come from the repo kind")
	}
	if _, err := Compile(ledger.Assumption{Statement: "config.yaml 存在"}, repo); err == nil {
		t.Fatal("empty assumption id must error")
	}
}
