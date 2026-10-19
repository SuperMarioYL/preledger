/* Preledger 假设台账评审界面。无框架、无外部依赖：fetch + SSE。 */
"use strict";

const STATUS_TEXT = { open: "待定", confirmed: "已确认", refuted: "已证伪" };

const state = {
  filter: "open",
  items: [],
  counts: { open: 0, confirmed: 0, refuted: 0 },
  repo: "",
  ledgerPath: "",
};

async function load() {
  const res = await fetch("/api/assumptions");
  if (!res.ok) return;
  const data = await res.json();
  state.items = data.assumptions || [];
  state.counts = data.counts || {};
  state.repo = data.repo || "";
  state.ledgerPath = data.ledger_path || "";
  render();
}

function visible() {
  if (state.filter === "all") return state.items;
  return state.items.filter((a) => a.status === state.filter);
}

function groupKey(a) {
  const ref = a.edit_ref || {};
  return [ref.session_id || "-", ref.tool || "-", ref.file || "-", ref.hunk || ""].join("\u0001");
}

function render() {
  document.getElementById("repo").textContent = state.repo
    ? `仓库 ${state.repo} · ${state.ledgerPath}`
    : state.ledgerPath;
  document.getElementById("count-open").textContent = state.counts.open || 0;
  document.getElementById("count-confirmed").textContent = state.counts.confirmed || 0;
  document.getElementById("count-refuted").textContent = state.counts.refuted || 0;
  document.querySelectorAll("#filters button").forEach((b) => {
    b.classList.toggle("active", b.dataset.filter === state.filter);
  });

  const items = visible();
  const empty = document.getElementById("empty");
  empty.hidden = items.length > 0;
  if (items.length === 0) {
    empty.innerHTML =
      state.items.length === 0
        ? '台账还没有记录。先在仓库里运行 <code>preledger install</code>，让 Claude Code 正常编辑；每次编辑背后的未验证假设会自动出现在这里。'
        : "当前筛选下暂无记录。";
  }

  const groups = new Map();
  for (const a of items) {
    const key = groupKey(a);
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(a);
  }

  const list = document.getElementById("list");
  list.replaceChildren();
  for (const [, entries] of groups) {
    const ref = entries[0].edit_ref || {};
    const group = document.createElement("section");
    group.className = "group";

    const head = document.createElement("div");
    head.className = "group-head";
    head.append(
      chip(`会话 ${(ref.session_id || "-").slice(0, 8)}`),
      chip(ref.tool || "edit"),
      chip(ref.file || "-"),
    );
    if (ref.hunk) head.append(chip(ref.hunk));
    group.append(head);

    for (const a of entries) group.append(card(a));
    list.append(group);
  }
}

function chip(text) {
  const el = document.createElement("code");
  el.textContent = text;
  return el;
}

function card(a) {
  const el = document.createElement("article");
  el.className = `card ${a.status}`;

  const row = document.createElement("div");
  row.className = "row";
  const badge = document.createElement("span");
  badge.className = `badge ${a.status}`;
  badge.textContent = STATUS_TEXT[a.status] || a.status;
  row.append(badge);
  if (!a.checkable && a.status !== "refuted") {
    const hint = document.createElement("span");
    hint.className = "hint";
    hint.textContent = "该前提暂无机械化检查，证伪会生成待人工复核的测试占位";
    row.append(hint);
  }
  el.append(row);

  const stmt = document.createElement("div");
  stmt.className = "statement";
  stmt.textContent = a.statement;
  el.append(stmt);

  if (a.basis) {
    const basis = document.createElement("blockquote");
    basis.className = "basis";
    basis.textContent = a.basis;
    el.append(basis);
  }

  if (a.refutation) {
    const info = document.createElement("div");
    info.className = "refutation";
    info.textContent = "失败测试已写入 ";
    const code = document.createElement("code");
    code.textContent = a.refutation.test_path || "";
    info.append(code);
    if (a.refutation.test_name) {
      info.append(`（${a.refutation.test_name}）`);
    }
    el.append(info);
  }

  const actions = document.createElement("div");
  actions.className = "row";
  if (a.status === "open") {
    actions.append(action("确认", "primary", () => setStatus(a.id, "confirmed")));
    actions.append(action("证伪", "danger", () => refute(a.id)));
  } else {
    actions.append(action("恢复待定", "", () => setStatus(a.id, "open")));
  }
  actions.append(spacer());
  el.append(actions);
  return el;
}

function action(label, cls, fn) {
  const b = document.createElement("button");
  b.className = `action ${cls}`;
  b.textContent = label;
  b.addEventListener("click", async (ev) => {
    b.disabled = true;
    try {
      await fn();
    } finally {
      b.disabled = false;
    }
  });
  return b;
}

function spacer() {
  const el = document.createElement("span");
  el.className = "spacer";
  return el;
}

async function setStatus(id, status) {
  const res = await fetch(`/api/assumptions/${id}/status`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ status }),
  });
  if (!res.ok) return toast(`操作失败：${(await errText(res)) || res.status}`);
  toast(`已标记为「${STATUS_TEXT[status]}」`);
  await load();
}

async function refute(id) {
  const res = await fetch(`/api/assumptions/${id}/refute`, { method: "POST" });
  if (!res.ok) return toast(`证伪失败：${(await errText(res)) || res.status}`);
  const a = await res.json();
  const testPath = a && a.refutation && a.refutation.test_path;
  toast(
    testPath
      ? `已证伪，失败测试已写入 ${testPath}\n运行测试套件查看红灯；修复代码后转绿。`
      : "已证伪。",
  );
  await load();
}

async function errText(res) {
  try {
    const body = await res.json();
    return body.error || "";
  } catch {
    return "";
  }
}

let toastTimer = null;
function toast(text) {
  const el = document.getElementById("toast");
  document.getElementById("toast-text").textContent = text;
  el.classList.add("show");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.classList.remove("show"), 5000);
}

document.querySelectorAll("#filters button").forEach((b) => {
  b.addEventListener("click", () => {
    state.filter = b.dataset.filter;
    render();
  });
});

// hook 持续追加台账：SSE 通知后重拉列表；连接断开时退回轮询。
function listen() {
  const es = new EventSource("/api/events");
  es.addEventListener("change", load);
  es.onerror = () => {
    es.close();
    setTimeout(listen, 3000);
  };
}

load();
listen();
