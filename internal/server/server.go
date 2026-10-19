// Package server serves the local zh-first review UI (m2) over the
// assumption ledger.
//
// "preledger up" binds 127.0.0.1 only: the reviewer triages entries in the
// browser while the agent keeps editing. fsnotify watches the ledger file and
// pushes a Server-Sent Events "change" notice whenever a hook appends to it,
// so the open page stays live without polling. Status writes and refutations
// go through the JSON API; 证伪 compiles a test into the repo via
// internal/refute and records the outcome in the ledger.
package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"

	"github.com/SuperMarioYL/preledger/internal/ledger"
	"github.com/SuperMarioYL/preledger/internal/refute"
)

//go:embed assets
var embedded embed.FS

// Server is the review UI plus its JSON API over one ledger store.
type Server struct {
	store    *ledger.Store
	repoRoot string
	repoName string

	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

// New returns a Server reading store and compiling refutations into repoRoot.
func New(store *ledger.Store, repoRoot string) *Server {
	return &Server{
		store:    store,
		repoRoot: repoRoot,
		repoName: filepath.Base(repoRoot),
		subs:     make(map[chan struct{}]struct{}),
	}
}

// Handler returns the HTTP handler: embedded assets plus the JSON API.
func (s *Server) Handler() http.Handler {
	assets, err := fs.Sub(embedded, "assets")
	if err != nil {
		panic("server: embedded assets missing: " + err.Error())
	}
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", http.FileServer(http.FS(assets)))
	mux.Handle("GET /app.js", http.FileServer(http.FS(assets)))
	mux.HandleFunc("GET /api/assumptions", s.handleList)
	mux.HandleFunc("POST /api/assumptions/{id}/status", s.handleStatus)
	mux.HandleFunc("POST /api/assumptions/{id}/refute", s.handleRefute)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	return mux
}

// assumptionView is the API shape of one ledger entry, annotated with whether
// a refutation would compile into a real assertion (vs a TODO stub).
type assumptionView struct {
	ledger.Assumption
	Checkable bool `json:"checkable"`
}

func view(a ledger.Assumption) assumptionView {
	_, checkable := refute.ParsePremise(a.Statement, a.Basis)
	return assumptionView{Assumption: a, Checkable: checkable}
}

// listResponse is the full state the UI needs per load.
type listResponse struct {
	Repo        string           `json:"repo"`
	LedgerPath  string           `json:"ledger_path"`
	Assumptions []assumptionView `json:"assumptions"`
	Counts      map[string]int   `json:"counts"`
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	current, err := s.store.Current()
	if err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	sort.SliceStable(current, func(i, j int) bool {
		return statusRank(current[i].Status) < statusRank(current[j].Status)
	})

	views := make([]assumptionView, len(current))
	counts := map[string]int{ledger.StatusOpen: 0, ledger.StatusConfirmed: 0, ledger.StatusRefuted: 0}
	for i, a := range current {
		views[i] = view(a)
		counts[a.Status]++
	}
	writeJSON(w, listResponse{
		Repo:        s.repoName,
		LedgerPath:  s.store.Path(),
		Assumptions: views,
		Counts:      counts,
	})
}

func statusRank(status string) int {
	switch status {
	case ledger.StatusOpen:
		return 0
	case ledger.StatusConfirmed:
		return 1
	default:
		return 2
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, http.StatusBadRequest, errors.New("请求体需为 {\"status\": ...}"))
		return
	}
	switch body.Status {
	case ledger.StatusOpen, ledger.StatusConfirmed:
	case ledger.StatusRefuted:
		httpError(w, http.StatusBadRequest, errors.New("证伪请使用 /api/assumptions/{id}/refute，以生成测试"))
		return
	default:
		httpError(w, http.StatusBadRequest, errors.New("未知状态 "+body.Status))
		return
	}
	if err := s.store.SetStatus(r.PathValue("id"), body.Status); err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, s.findView(r.PathValue("id")))
}

func (s *Server) handleRefute(w http.ResponseWriter, r *http.Request) {
	a, err := refute.Refute(s.store, r.PathValue("id"), s.repoRoot)
	if err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, view(a))
}

func (s *Server) findView(id string) *assumptionView {
	current, err := s.store.Current()
	if err != nil {
		return nil
	}
	for _, a := range current {
		if a.ID == id {
			v := view(a)
			return &v
		}
	}
	return nil
}

// handleEvents streams Server-Sent Events: a "change" event whenever the
// ledger file changes, telling open pages to reload.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	ch := s.subscribe()
	defer s.unsubscribe(ch)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(": connected\n\n"))
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			_, _ = w.Write([]byte("event: change\ndata: {}\n\n"))
			flusher.Flush()
		}
	}
}

func (s *Server) subscribe() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan struct{}, 1)
	s.subs[ch] = struct{}{}
	return ch
}

func (s *Server) unsubscribe(ch chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subs, ch)
}

func (s *Server) notify() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default: // a pending notice already covers the next reload
		}
	}
}

// Watch watches the ledger file with fsnotify until ctx is done and notifies
// SSE subscribers on every change. The file may not exist yet (no capture
// has run), so the directory is watched first and the file watch is (re-)
// added whenever the ledger appears.
func (s *Server) Watch(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	ledgerPath := s.store.Path()
	if err := watcher.Add(filepath.Dir(ledgerPath)); err != nil {
		return err
	}
	_ = watcher.Add(ledgerPath) // best effort until the first append creates it

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if event.Name != ledgerPath {
				continue
			}
			_ = watcher.Add(ledgerPath) // recreate after remove
			s.notify()
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			return err
		}
	}
}

// RepoRoot exposes the repo the server compiles refutations into.
func (s *Server) RepoRoot() string { return s.repoRoot }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func httpError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": strings.TrimSpace(err.Error())})
}
