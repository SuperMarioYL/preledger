// Command preledger is the assumption ledger for coding-agent edits.
//
// One binary, three subcommands (per the plan's architecture):
//
//	preledger install   接入 Claude Code hook 并搭建 .preledger/ 台账目录
//	preledger hook      PostToolUse 钩子入口：读取 stdin 载荷，解析转录并追加假设
//	preledger up        启动本地中文评审界面（127.0.0.1:7830）
//
// Hook invocations are short-lived child processes started by Claude Code and
// must never block an edit: the hook command always exits 0, printing capture
// failures to stderr. "up" is the only long-lived process.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/SuperMarioYL/preledger/internal/config"
	"github.com/SuperMarioYL/preledger/internal/extract"
	"github.com/SuperMarioYL/preledger/internal/hook"
	"github.com/SuperMarioYL/preledger/internal/ledger"
	"github.com/SuperMarioYL/preledger/internal/server"
	"github.com/SuperMarioYL/preledger/internal/transcript"
)

// version is the release version; keep it in sync with the VERSION file.
const version = "0.1.0"

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:          "preledger",
		Short:        "编码代理编辑的假设台账：编辑时记录、本地评审、证伪即测试",
		Version:      version,
		SilenceUsage: true,
	}
	root.AddCommand(newInstallCmd(), newHookCmd(), newUpCmd())
	return root
}

func newInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "把 PostToolUse hook 接入 .claude/settings.json 并搭建 .preledger/ 台账",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if err := scaffoldLedger(cfg); err != nil {
				return err
			}
			wired, err := wireHook(cfg)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if wired {
				fmt.Fprintf(out, "已接入 Claude Code hook：%s (PostToolUse: %s)\n", cfg.ClaudeSettingsPath(), hook.Matcher)
			} else {
				fmt.Fprintf(out, "hook 已存在，跳过：%s\n", cfg.ClaudeSettingsPath())
			}
			fmt.Fprintf(out, "台账目录：%s\n", cfg.LedgerDir())
			fmt.Fprintf(out, "下一步：让 Claude Code 正常编辑，然后运行 preledger up 评审假设\n")
			return nil
		},
	}
}

// scaffoldLedger creates the .preledger directory. The ledger itself is
// created by the first append; .gitkeep keeps the empty directory committable
// because the ledger travels with the repo.
func scaffoldLedger(cfg config.Config) error {
	if err := os.MkdirAll(cfg.LedgerDir(), 0o755); err != nil {
		return fmt.Errorf("创建台账目录: %w", err)
	}
	gitkeep := filepath.Join(cfg.LedgerDir(), ".gitkeep")
	if _, err := os.Stat(gitkeep); os.IsNotExist(err) {
		if err := os.WriteFile(gitkeep, nil, 0o644); err != nil {
			return fmt.Errorf("写入台账目录占位: %w", err)
		}
	}
	return nil
}

// wireHook merges the PostToolUse hook into the project settings file,
// preserving every existing key. Idempotent: a Preledger entry already
// present is left alone.
func wireHook(cfg config.Config) (bool, error) {
	exe, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("定位可执行文件: %w", err)
	}
	command := strconv.Quote(exe) + " hook"

	settings := map[string]any{}
	if raw, err := os.ReadFile(cfg.ClaudeSettingsPath()); err == nil {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return false, fmt.Errorf("%s 不是有效的 JSON: %w", cfg.ClaudeSettingsPath(), err)
		}
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	groups, _ := hooks[hook.EventPostToolUse].([]any)
	for _, g := range groups {
		group, _ := g.(map[string]any)
		entries, _ := group["hooks"].([]any)
		for _, e := range entries {
			entry, _ := e.(map[string]any)
			if c, _ := entry["command"].(string); strings.Contains(c, "preledger") {
				return false, nil
			}
		}
	}
	newGroups := hook.HooksConfig(command)[hook.EventPostToolUse].([]any)
	hooks[hook.EventPostToolUse] = append(groups, newGroups[0])
	settings["hooks"] = hooks

	if err := os.MkdirAll(filepath.Dir(cfg.ClaudeSettingsPath()), 0o755); err != nil {
		return false, fmt.Errorf("创建 .claude 目录: %w", err)
	}
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(cfg.ClaudeSettingsPath(), append(encoded, '\n'), 0o644); err != nil {
		return false, fmt.Errorf("写入 hook 配置: %w", err)
	}
	return true, nil
}

func newHookCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "hook",
		Short: "Claude Code PostToolUse 钩子入口（由 install 自动接线，不需要手动调用）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return captureFailure(err)
			}
			if _, err := runCapture(cmd.Context(), cfg, cmd.InOrStdin()); err != nil {
				// 捕获失败绝不能阻塞编辑：记录到 stderr，仍以 0 退出。
				_ = captureFailure(err)
			}
			return nil
		},
	}
}

// runCapture is the hook pipeline: parse the payload, record the mechanical
// premise, layer the heuristic extractor over the transcript context, run the
// optional triage, then append everything to the ledger.
func runCapture(ctx context.Context, cfg config.Config, stdin io.Reader) ([]ledger.Assumption, error) {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return nil, fmt.Errorf("read hook stdin: %w", err)
	}
	var payload hook.Payload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("parse hook payload: %w", err)
	}

	base, _ := os.Getwd()
	entries, err := hook.Capture(payload, base)
	if err != nil {
		return entries, err
	}

	var in hook.EditInput
	if len(payload.ToolInput) > 0 {
		if err := json.Unmarshal(payload.ToolInput, &in); err != nil {
			return entries, fmt.Errorf("parse tool_input: %w", err)
		}
	}
	quotes, _ := transcript.AssistantTextsBefore(payload.TranscriptPath, payload.ToolUseID, 8)
	extra := extract.FromEdit(extract.Edit{
		Ref:     entriesRef(entries, payload),
		Tool:    payload.ToolName,
		File:    entriesFile(entries),
		NewText: newTextOf(in, payload.ToolName),
	}, cfg.RepoRoot, quotes)
	entries = append(entries, extra...)

	triageCtx, cancel := context.WithTimeout(ctx, triageTimeout)
	triaged, err := extract.Triage(triageCtx, cfg.Triage, entries)
	cancel()
	if err != nil {
		// triage 失败时保留全部候选（离线优先）。
		triaged = entries
	}
	entries = triaged

	store := ledger.Open(cfg.LedgerPath())
	for _, a := range entries {
		if err := store.Append(a); err != nil {
			return entries, fmt.Errorf("append to ledger: %w", err)
		}
	}
	return entries, nil
}

// entriesRef takes the edit ref produced by hook.Capture; when the payload
// was not an edit, the heuristic path has nothing to work with either.
func entriesRef(entries []ledger.Assumption, payload hook.Payload) ledger.EditRef {
	if len(entries) > 0 {
		return entries[0].EditRef
	}
	return ledger.EditRef{SessionID: payload.SessionID, Tool: payload.ToolName}
}

// entriesFile prefers the repo-relative file from the captured entry.
func entriesFile(entries []ledger.Assumption) string {
	if len(entries) > 0 {
		return entries[0].EditRef.File
	}
	return ""
}

func newTextOf(in hook.EditInput, tool string) string {
	if tool == hook.ToolWrite {
		return in.Content
	}
	return in.NewString
}

// triageCtx bounds the optional LLM call well under the hook's 10s timeout;
// on expiry the caller keeps every candidate.
const triageTimeout = 5 * time.Second

// captureFailure reports a capture error to stderr without failing the hook
// process: Claude Code treats a non-zero hook exit as a reason to nag the
// user, and a capture hiccup must never block an edit.
func captureFailure(err error) error {
	fmt.Fprintf(os.Stderr, "preledger hook：%v\n", err)
	return nil
}

func newUpCmd() *cobra.Command {
	var port int
	var noOpen bool
	cmd := &cobra.Command{
		Use:   "up",
		Short: "启动本地中文评审界面（默认 http://127.0.0.1:7830）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("port") {
				cfg.Port = port
			}
			store := ledger.Open(cfg.LedgerPath())
			if current, err := store.Current(); err == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "台账：%s（%d 条假设）\n", cfg.LedgerPath(), len(current))
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			srv := server.New(store, cfg.RepoRoot)
			go func() {
				if err := srv.Watch(ctx); err != nil && ctx.Err() == nil {
					fmt.Fprintf(os.Stderr, "preledger up：台账监听不可用（%v），界面仍可手动刷新\n", err)
				}
			}()

			addr := fmt.Sprintf("127.0.0.1:%d", cfg.Port)
			url := "http://" + addr
			fmt.Fprintf(cmd.OutOrStdout(), "评审界面已启动：%s（Ctrl-C 退出）\n", url)
			if !noOpen && os.Getenv("PRELEDGER_NO_OPEN") == "" {
				openBrowser(url)
			}

			httpSrv := &http.Server{Addr: addr, Handler: srv.Handler(), ReadHeaderTimeout: 5 * time.Second}
			errCh := make(chan error, 1)
			go func() { errCh <- httpSrv.ListenAndServe() }()
			select {
			case <-ctx.Done():
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = httpSrv.Shutdown(shutdownCtx)
				fmt.Fprintln(cmd.OutOrStdout(), "评审界面已关闭")
				return nil
			case err := <-errCh:
				if err == http.ErrServerClosed {
					return nil
				}
				return fmt.Errorf("启动评审界面: %w", err)
			}
		},
	}
	cmd.Flags().IntVar(&port, "port", config.DefaultPort, "评审界面端口（也可用 PRELEDGER_PORT）")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "不自动打开浏览器")
	return cmd
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "preledger up：自动打开浏览器失败（%v），请手动访问 %s\n", err, url)
	}
	_ = cmd.Process.Release()
}
