package extract

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuperMarioYL/preledger/internal/config"
	"github.com/SuperMarioYL/preledger/internal/ledger"
	"github.com/SuperMarioYL/preledger/internal/refute"
)

func goRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module sample\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func edit(tool, file, newText string) Edit {
	return Edit{
		Ref:     ledger.EditRef{SessionID: "s1", ToolUseID: "toolu_1", Tool: tool, File: file},
		Tool:    tool,
		File:    file,
		NewText: newText,
	}
}

func TestMissingGoDependencyPremise(t *testing.T) {
	root := goRepo(t)
	e := edit("Edit", "main.go", "import (\n\t\"fmt\"\n\t\"github.com/pkg/errors\"\n)\n")
	got := FromEdit(e, root, nil)
	if len(got) != 1 {
		t.Fatalf("want 1 premise, got %v", premises(got))
	}
	a := got[0]
	if a.Statement != "go.mod 存在且含 github.com/pkg/errors" {
		t.Errorf("statement = %q", a.Statement)
	}
	if a.Basis != `"github.com/pkg/errors"` {
		t.Errorf("basis = %q", a.Basis)
	}
	if _, ok := refute.ParsePremise(a.Statement, a.Basis); !ok {
		t.Errorf("premise not mechanically checkable: %q", a.Statement)
	}
}

func TestKnownGoDependencyEmitsNothing(t *testing.T) {
	root := goRepo(t)
	mod := "module sample\n\nrequire github.com/pkg/errors v0.9.1\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	e := edit("Edit", "main.go", "import \"github.com/pkg/errors\"\n")
	if got := FromEdit(e, root, nil); len(got) != 0 {
		t.Fatalf("declared dep must not premise, got %v", premises(got))
	}
	// A parent module covering the import path also counts as declared.
	root2 := goRepo(t)
	mod2 := "module sample\n\nrequire github.com/pkg v1.0.0\n"
	if err := os.WriteFile(filepath.Join(root2, "go.mod"), []byte(mod2), 0o644); err != nil {
		t.Fatal(err)
	}
	e2 := edit("Edit", "main.go", "import \"github.com/pkg/errors\"\n")
	if got := FromEdit(e2, root2, nil); len(got) != 0 {
		t.Fatalf("parent module must satisfy premise, got %v", premises(got))
	}
}

func TestNodeDependencyPremise(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"dependencies":{"zod":"^3.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	e := edit("Edit", "src/app.ts", "import { plainToClass } from \"class-transformer\";\nimport { z } from \"zod\";\nconst p = require(\"node:fs\");\n")
	got := FromEdit(e, root, nil)
	if len(got) != 1 || got[0].Statement != "package.json 存在且含 class-transformer" {
		t.Fatalf("premises = %v", premises(got))
	}
}

func TestConfigKeyPremiseWithAgentQuote(t *testing.T) {
	root := goRepo(t)
	e := edit("Edit", "main.go",
		"// 从 config.yaml 读取\n\tfmt.Println(loadKey(\"server.port\"))\n\tos.ReadFile(\"config.yaml\")\n")
	said := []string{"好的，服务端口走 config.yaml 的 server.port，我直接假定它存在。"}
	got := FromEdit(e, root, said)
	if len(got) != 1 {
		t.Fatalf("want 1 premise, got %v", premises(got))
	}
	a := got[0]
	if a.Statement != "config.yaml 存在且含 server.port" {
		t.Errorf("statement = %q", a.Statement)
	}
	if !strings.Contains(a.Basis, "server.port") || strings.Contains(a.Basis, "loadKey") {
		t.Errorf("basis should quote the agent, got %q", a.Basis)
	}
	if _, ok := refute.ParsePremise(a.Statement, a.Basis); !ok {
		t.Errorf("premise not mechanically checkable: %q", a.Statement)
	}
}

func TestConfigPremiseFallsBackToCodeLine(t *testing.T) {
	root := goRepo(t)
	e := edit("Edit", "main.go", "data, err := os.ReadFile(\"config.yaml\")\n")
	got := FromEdit(e, root, nil)
	if len(got) != 1 || got[0].Statement != "config.yaml 存在" {
		t.Fatalf("premises = %v", premises(got))
	}
	if !strings.Contains(got[0].Basis, "config.yaml") {
		t.Errorf("basis = %q", got[0].Basis)
	}
}

func TestExistingConfigPathEmitsNothing(t *testing.T) {
	root := goRepo(t)
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte("server:\n  port: 8080\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := edit("Edit", "main.go", "data, err := os.ReadFile(\"config.yaml\")\n\t_ = data\n\t_ = err\n")
	if got := FromEdit(e, root, nil); len(got) != 0 {
		t.Fatalf("existing config must not premise, got %v", premises(got))
	}
}

func TestKeyDeclaredElsewhereEmitsNothing(t *testing.T) {
	root := goRepo(t)
	if err := os.WriteFile(filepath.Join(root, "settings.yaml"), []byte("server.port: 8080\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := edit("Edit", "main.go", "// 与 settings.yaml 相同约定\n\twant(\"server.port\")\n\tread(\"missing.yaml\")\n")
	got := FromEdit(e, root, nil)
	for _, a := range got {
		if a.Statement == "missing.yaml 存在且含 server.port" {
			t.Errorf("key declared in settings.yaml must not premise on missing.yaml: %v", premises(got))
		}
	}
}

func TestEnvKeyPremise(t *testing.T) {
	root := goRepo(t)
	e := edit("Edit", "main.go", "token := os.Getenv(\"API_TOKEN\")\n\t_ = token\n")
	got := FromEdit(e, root, nil)
	if len(got) != 1 || got[0].Statement != ".env 存在且含 API_TOKEN" {
		t.Fatalf("premises = %v", premises(got))
	}
}

func TestWriteNewPathPremiseViaGit(t *testing.T) {
	root := goRepo(t)
	run(t, root, "init", "-q")
	// hook 在 Write 落盘之后触发：文件已存在，git 视其为未跟踪新路径。
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "deploy", "config.yaml"), []byte("server:\n  port: 8080\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := edit("Write", "deploy/config.yaml", "server:\n  port: 8080\n")
	got := FromEdit(e, root, nil)
	if len(got) != 1 || got[0].Statement != "deploy/config.yaml 存在" {
		t.Fatalf("premises = %v", premises(got))
	}
	if !strings.HasPrefix(got[0].Basis, "?? deploy/config.yaml") {
		t.Errorf("basis should be the porcelain line, got %q", got[0].Basis)
	}
}

func TestWriteStagedNewPathPremise(t *testing.T) {
	root := goRepo(t)
	run(t, root, "init", "-q")
	run(t, root, "add", "go.mod")
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, root, "add", "notes.md")
	e := edit("Write", "notes.md", "y\n")
	got := FromEdit(e, root, nil)
	if len(got) != 1 || got[0].Statement != "notes.md 存在" {
		t.Fatalf("staged-new path must premise: %v", premises(got))
	}
}

func TestWriteHeadTrackedPathEmitsNothing(t *testing.T) {
	root := goRepo(t)
	run(t, root, "init", "-q")
	run(t, root, "add", "go.mod")
	run(t, root, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-qm", "init")
	// go.mod 已在 HEAD 中：Write 更新的是已跟踪文件，不是新路径。
	e := edit("Write", "go.mod", "module sample\n\ngo 1.24\n\nrequire github.com/pkg/errors v0.9.1\n")
	if got := FromEdit(e, root, nil); len(got) != 0 {
		t.Fatalf("tracked file must not premise, got %v", premises(got))
	}
}

func TestMaxEntriesPerEdit(t *testing.T) {
	root := goRepo(t)
	var b strings.Builder
	b.WriteString("import (\n")
	for i := 0; i < 12; i++ {
		b.WriteString("\t\"github.com/pkg/pkg" + string(rune('a'+i)) + "\"\n")
	}
	b.WriteString(")\n")
	got := FromEdit(edit("Edit", "main.go", b.String()), root, nil)
	if len(got) > maxEntriesPerEdit {
		t.Fatalf("cap breached: %d entries", len(got))
	}
	if len(got) != maxEntriesPerEdit {
		t.Fatalf("want %d entries, got %d", maxEntriesPerEdit, len(got))
	}
}

func TestNonEditToolEmitsNothing(t *testing.T) {
	root := goRepo(t)
	e := edit("Bash", "main.go", "import \"github.com/pkg/errors\"\n")
	if got := FromEdit(e, root, nil); len(got) != 0 {
		t.Fatalf("non-edit tool produced %v", premises(got))
	}
}

func TestTriageFiltersOnProviderVerdict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if req.Model != "deepseek-v3" {
			t.Errorf("model = %q", req.Model)
		}
		if len(req.Messages) != 2 {
			t.Errorf("messages = %d", len(req.Messages))
		}
		w.Header().Set("Content-Type", "application/json")
		content := "```json\n" + `[{"index":0,"load_bearing":true},{"index":1,"load_bearing":false}]` + "\n```"
		verdict, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"message": map[string]string{"content": content}}},
		})
		_, _ = w.Write(verdict)
	}))
	defer srv.Close()

	cfg := config.Triage{Provider: "deepseek", APIKey: "test-key", BaseURL: srv.URL, Model: "deepseek-v3"}
	candidates := []ledger.Assumption{
		{ID: "a", Statement: "config.yaml 存在且含 server.port", Status: ledger.StatusOpen},
		{ID: "b", Statement: "README.md 存在", Status: ledger.StatusOpen},
	}
	kept, err := Triage(context.Background(), cfg, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0].ID != "a" {
		t.Fatalf("kept = %v", kept)
	}
}

func TestTriageDegradesToKeepAll(t *testing.T) {
	candidates := []ledger.Assumption{
		{ID: "a", Statement: "s1", Status: ledger.StatusOpen},
		{ID: "b", Statement: "s2", Status: ledger.StatusOpen},
	}
	// 无 provider：原样通过。
	kept, err := Triage(context.Background(), config.Triage{}, candidates)
	if err != nil || len(kept) != 2 {
		t.Fatalf("disabled triage: kept=%v err=%v", kept, err)
	}
	// 服务端报错：降级为全部保留。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	cfg := config.Triage{Provider: "glm", APIKey: "k", BaseURL: srv.URL, Model: "glm-4.6"}
	kept, err = Triage(context.Background(), cfg, candidates)
	if err == nil || len(kept) != 2 {
		t.Fatalf("failing triage must keep all: kept=%v err=%v", kept, err)
	}
}

func TestTriageKeepsResolvedRecords(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"[{\"index\":0,\"load_bearing\":false},{\"index\":1,\"load_bearing\":false}]"}}]}`))
	}))
	defer srv.Close()
	cfg := config.Triage{Provider: "deepseek", APIKey: "k", BaseURL: srv.URL, Model: "m"}
	candidates := []ledger.Assumption{
		{ID: "a", Statement: "s1", Status: ledger.StatusOpen},
		{ID: "b", Statement: "s2", Status: ledger.StatusConfirmed},
	}
	kept, err := Triage(context.Background(), cfg, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0].ID != "b" {
		t.Fatalf("resolved records must survive: %v", kept)
	}
}

func premises(list []ledger.Assumption) []string {
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = a.Statement
	}
	return out
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
