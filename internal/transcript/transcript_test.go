package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleTranscript = `{"type":"user","message":{"role":"user","content":"给 sample 服务加个端口配置"}}
{"type":"assistant","message":{"content":[{"type":"text","text":"我先确认 config.yaml 的约定。"},{"type":"tool_use","id":"toolu_read","name":"Read","input":{"file_path":"/repo/main.go"}}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"配置文件不存在，我会假定 config.yaml 含 server.port。"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"现在写入读取逻辑。"},{"type":"tool_use","id":"toolu_edit","name":"Edit","input":{"file_path":"/repo/main.go","old_string":"a","new_string":"b"}}]}}
not json at all
`

func writeSample(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadKeepsAssistantEntriesInOrder(t *testing.T) {
	entries, err := Load(writeSample(t, sampleTranscript))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("want 3 assistant entries, got %d", len(entries))
	}
	if !strings.Contains(entries[0].Text, "config.yaml 的约定") {
		t.Errorf("first entry text = %q", entries[0].Text)
	}
	if len(entries[0].ToolUseIDs) != 1 || entries[0].ToolUseIDs[0] != "toolu_read" {
		t.Errorf("first entry tool uses = %v", entries[0].ToolUseIDs)
	}
}

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	entries, err := Load(filepath.Join(t.TempDir(), "absent.jsonl"))
	if err != nil || entries != nil {
		t.Fatalf("missing transcript: entries=%v err=%v", entries, err)
	}
}

func TestAssistantTextsBeforeEdit(t *testing.T) {
	texts, err := AssistantTextsBefore(writeSample(t, sampleTranscript), "toolu_edit", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(texts) != 2 {
		t.Fatalf("want 2 texts before the edit, got %d: %v", len(texts), texts)
	}
	if !strings.Contains(texts[1], "server.port") {
		t.Errorf("second text = %q", texts[1])
	}
}

func TestAssistantTextsBeforeFallsBackToTail(t *testing.T) {
	path := writeSample(t, sampleTranscript)
	texts, err := AssistantTextsBefore(path, "toolu_missing", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(texts) != 1 || !strings.Contains(texts[0], "写入读取逻辑") {
		t.Fatalf("fallback tail = %v", texts)
	}
	empty := filepath.Join(t.TempDir(), "none.jsonl")
	if texts, err = AssistantTextsBefore(empty, "", 3); err != nil || len(texts) != 0 {
		t.Fatalf("missing file: texts=%v err=%v", texts, err)
	}
}

func TestLoadAcceptsStringContent(t *testing.T) {
	path := writeSample(t, `{"type":"assistant","message":{"content":"纯字符串内容块"}}`+"\n")
	entries, err := Load(path)
	if err != nil || len(entries) != 1 || entries[0].Text != "纯字符串内容块" {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
}
