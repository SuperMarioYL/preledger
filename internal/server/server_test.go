package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuperMarioYL/preledger/internal/ledger"
)

func seedRepo(t *testing.T) (*ledger.Store, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module sample\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := ledger.Open(filepath.Join(root, ".preledger", "ledger.jsonl"))
	a := ledger.Assumption{
		ID:        "abcdef0123456789",
		EditRef:   ledger.EditRef{SessionID: "s1", Tool: "Edit", File: "main.go"},
		Statement: "config.yaml 存在且含 server.port",
		Basis:     "我会假定 config.yaml 含 server.port。",
		Status:    ledger.StatusOpen,
	}
	if err := store.Append(a); err != nil {
		t.Fatal(err)
	}
	return store, root
}

func TestHandlerServesEmbeddedUI(t *testing.T) {
	store, root := seedRepo(t)
	srv := httptest.NewServer(New(store, root).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "假设台账") {
		t.Fatalf("index: status=%d body=%.80s", resp.StatusCode, body)
	}
	if resp, err = http.Get(srv.URL + "/app.js"); err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("app.js status=%d", resp.StatusCode)
	}
}

func TestListSortsOpenFirstAndCounts(t *testing.T) {
	store, root := seedRepo(t)
	if err := store.Append(ledger.Assumption{
		ID: "1111111111111111", EditRef: ledger.EditRef{SessionID: "s1", File: "a.go"},
		Statement: "README.md 存在且含 x", Status: ledger.StatusConfirmed,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ledger.Assumption{
		ID: "2222222222222222", EditRef: ledger.EditRef{SessionID: "s1", File: "b.go"},
		Statement: "tmp 存在", Status: ledger.StatusRefuted,
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(store, root).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/assumptions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var data listResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatal(err)
	}
	if len(data.Assumptions) != 3 {
		t.Fatalf("assumptions = %d", len(data.Assumptions))
	}
	if data.Assumptions[0].Status != ledger.StatusOpen {
		t.Errorf("open entries must sort first, got %s", data.Assumptions[0].Status)
	}
	if data.Counts[ledger.StatusOpen] != 1 || data.Counts[ledger.StatusConfirmed] != 1 || data.Counts[ledger.StatusRefuted] != 1 {
		t.Errorf("counts = %v", data.Counts)
	}
	// config.yaml 不在临时仓库中：该前提可机械化验证。
	if !data.Assumptions[0].Checkable {
		t.Error("config.yaml premise should be checkable")
	}
}

func TestStatusEndpointConfirmsAndRejectsRefuted(t *testing.T) {
	store, root := seedRepo(t)
	srv := httptest.NewServer(New(store, root).Handler())
	defer srv.Close()

	post := func(path, body string) (*http.Response, string) {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp, string(data)
	}

	resp, body := post("/api/assumptions/abcdef0123456789/status", `{"status":"confirmed"}`)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"status":"confirmed"`) {
		t.Fatalf("confirm: %d %s", resp.StatusCode, body)
	}
	resp, body = post("/api/assumptions/abcdef0123456789/status", `{"status":"refuted"}`)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "refute") {
		t.Fatalf("refuted via status must redirect to refute API: %d %s", resp.StatusCode, body)
	}
	resp, body = post("/api/assumptions/missing/status", `{"status":"confirmed"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown id: %d %s", resp.StatusCode, body)
	}
}

func TestRefuteEndpointCompilesTestIntoRepo(t *testing.T) {
	store, root := seedRepo(t)
	srv := httptest.NewServer(New(store, root).Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/assumptions/abcdef0123456789/refute", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var a assumptionView
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		t.Fatal(err)
	}
	if a.Status != ledger.StatusRefuted || a.Refutation == nil {
		t.Fatalf("refute response: %+v", a)
	}
	if _, err := os.Stat(filepath.Join(root, a.Refutation.TestPath)); err != nil {
		t.Fatalf("generated test missing: %v", err)
	}
	// 台账保留证伪记录（append-only）。
	current, err := store.Current()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range current {
		if entry.ID == "abcdef0123456789" && entry.Status == ledger.StatusRefuted {
			found = true
		}
	}
	if !found {
		t.Error("ledger must keep the refutation record")
	}
}

func TestEventsStreamNotifiesOnChange(t *testing.T) {
	store, root := seedRepo(t)
	s := New(store, root)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	done := make(chan error, 1)
	watchCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { done <- s.Watch(watchCtx) }()

	resp, err := http.Get(srv.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	events := make(chan string, 1)
	go func() {
		buf := make([]byte, 256)
		var pending []byte
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				pending = append(pending, buf[:n]...)
				if strings.Contains(string(pending), "event: change") {
					events <- "change"
					return
				}
			}
			if err != nil {
				events <- "closed"
				return
			}
		}
	}()

	// 先等待 SSE 订阅建立，再追加台账触发通知。
	deadline := time.Now().Add(2 * time.Second)
	for len(s.subs) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := store.Append(ledger.Assumption{
		ID: "3333333333333333", EditRef: ledger.EditRef{SessionID: "s2", File: "c.go"},
		Statement: "x.yaml 存在", Status: ledger.StatusOpen,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-events:
		if got != "change" {
			t.Fatalf("stream closed early: %s", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no SSE change event after ledger append")
	}
}
