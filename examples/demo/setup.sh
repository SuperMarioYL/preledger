#!/bin/sh
# 搭建 Preledger 演示仓库：一个最小 Go 服务，加上三段与 Claude Code hook
# stdin 完全同构的载荷（PostToolUse / Edit|Write），以及一段真实的转录摘录。
#
# 用法：bash examples/demo/setup.sh [目标目录] [preledger 二进制路径]
#   目标目录默认 /tmp/preledger-demo；
#   二进制默认 bin/preledger（仓库根目录下 go build 的产物），会被复制进
#   演示仓库，演示中的命令都用 ./preledger 调用，不依赖 PATH。
#
# 生成的目录：
#   $DEMO       演示仓库（go.mod + main.go，已 git init）
#   $DEMO-files 控制文件：转录摘录、三段 hook 载荷、refute.py（评审界面
#               证伪按钮发出的同一个 POST）、statements.py（打印台账语句）
set -eu

DEMO="${1:-/tmp/preledger-demo}"
CTL="${DEMO}-files"
BIN="${2:-bin/preledger}"

rm -rf "$DEMO" "$CTL"
mkdir -p "$DEMO" "$CTL"

if [ ! -f "$BIN" ]; then
	echo "找不到 $BIN，请先 go build -o bin/preledger ." >&2
	exit 1
fi
cp "$BIN" "$DEMO/preledger"

TRANSCRIPT="$CTL/transcript-s1.jsonl"
SESSION="s1-demo-session"

# ---- 演示仓库 ----
cat > "$DEMO/go.mod" <<'EOF'
module sample

go 1.24
EOF

cat > "$DEMO/main.go" <<'EOF'
package main

import "fmt"

func main() {
	fmt.Println("sample service")
}
EOF

cd "$DEMO"
git init -q
git add go.mod main.go
git -c user.name="preledger-demo" -c user.email="demo@localhost" commit -qm "init sample service"

# ---- 转录摘录：助手在编辑前说出的假设 ----
cat > "$TRANSCRIPT" <<'EOF'
{"type":"user","message":{"role":"user","content":"给 sample 服务加上端口读取，从 config.yaml 来，密钥走环境变量"}}
{"type":"assistant","message":{"content":[{"type":"text","text":"配置文件还不存在，我会假定 config.yaml 存在且含 server.port，先写读取逻辑。"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"端口读取写好了。密钥我假定仓库里已经配置了 API_TOKEN。"},{"type":"tool_use","id":"toolu_a1","name":"Edit","input":{"file_path":"PLACEHOLDER/main.go"}}]}}
EOF
sed -i.bak "s|PLACEHOLDER|$DEMO|g" "$TRANSCRIPT" && rm -f "$TRANSCRIPT.bak"

# ---- 三段 hook 载荷（与 Claude Code 写给 PostToolUse 命令 stdin 的结构一致）----
python3 - "$DEMO" "$CTL" "$TRANSCRIPT" "$SESSION" <<'PYEOF'
import json, pathlib, sys

demo, ctl, transcript, session = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]

old_main = """package main

import "fmt"

func main() {
\tfmt.Println("sample service")
}
"""

new_main = """package main

import (
\t"fmt"
\t"os"
\t"strings"
)

func main() {
\tfmt.Println("sample service on", readPort())
}

func readPort() string {
\tdata, err := os.ReadFile("config.yaml")
\tif err != nil {
\t\treturn "8080"
\t}
\tfor _, line := range strings.Split(string(data), "\\n") {
\t\tif strings.Contains(line, "server.port") {
\t\t\treturn strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
\t\t}
\t}
\treturn "8080"
}
"""

token_main = """func main() {
\ttoken := os.Getenv("API_TOKEN")
\tif token == "" {
\t\ttoken = "dev"
\t}
\tfmt.Println("sample service on", readPort(), "token", token)
}
"""

old_token = """func main() {
\tfmt.Println("sample service on", readPort())
}
"""

def payload(tool_use_id, tool, tool_input, hunk=None):
    p = {
        "session_id": session,
        "transcript_path": transcript,
        "hook_event_name": "PostToolUse",
        "tool_use_id": tool_use_id,
        "tool_name": tool,
        "tool_input": tool_input,
    }
    if hunk:
        p["tool_response"] = {"structuredPatch": [hunk]}
    return p

payloads = [
    payload("toolu_a1", "Edit",
            {"file_path": f"{demo}/main.go", "old_string": old_main,
             "new_string": new_main, "replace_all": False},
            {"oldStart": 1, "oldLines": 7, "newStart": 1, "newLines": 22}),
    payload("toolu_a2", "Edit",
            {"file_path": f"{demo}/main.go", "old_string": old_token,
             "new_string": token_main, "replace_all": False},
            {"oldStart": 7, "oldLines": 3, "newStart": 7, "newLines": 7}),
    payload("toolu_a3", "Write",
            {"file_path": f"{demo}/README.md",
             "content": "# sample service\n\n演示仓库：验证 Preledger 的假设台账。\n"}),
]

for i, p in enumerate(payloads, 1):
    (pathlib.Path(ctl) / f"p{i}.json").write_text(
        json.dumps(p, ensure_ascii=False), encoding="utf-8")
PYEOF

# 像真实 agent 一样把三次编辑依次落到磁盘（hook 在每次编辑之后触发）。
python3 - "$DEMO" <<'PYEOF'
import pathlib, sys

demo = pathlib.Path(sys.argv[1])
main = demo / "main.go"
text = main.read_text(encoding="utf-8")

text = text.replace(
    'import "fmt"\n\nfunc main() {\n\tfmt.Println("sample service")\n}',
    'import (\n\t"fmt"\n\t"os"\n\t"strings"\n)\n\n'
    'func main() {\n\tfmt.Println("sample service on", readPort())\n}\n\n'
    'func readPort() string {\n\tdata, err := os.ReadFile("config.yaml")\n'
    '\tif err != nil {\n\t\treturn "8080"\n\t}\n'
    '\tfor _, line := range strings.Split(string(data), "\\n") {\n'
    '\t\tif strings.Contains(line, "server.port") {\n'
    '\t\t\treturn strings.TrimSpace(strings.SplitN(line, ":", 2)[1])\n\t\t}\n\t}\n\treturn "8080"\n}',
)
text = text.replace(
    'func main() {\n\tfmt.Println("sample service on", readPort())\n}',
    'func main() {\n\ttoken := os.Getenv("API_TOKEN")\n\tif token == "" {\n\t\ttoken = "dev"\n\t}\n'
    '\tfmt.Println("sample service on", readPort(), "token", token)\n}',
)
main.write_text(text, encoding="utf-8")
(demo / "README.md").write_text(
    "# sample service\n\n演示仓库：验证 Preledger 的假设台账。\n", encoding="utf-8")
PYEOF

# ---- 评审界面「证伪」按钮发出的同一个 POST ----
cat > "$CTL/refute.py" <<'EOF'
import json
import urllib.request

data = json.load(urllib.request.urlopen("http://127.0.0.1:7830/api/assumptions"))
target = next(
    a for a in data["assumptions"]
    if a["statement"] == "config.yaml 存在且含 server.port"
)
req = urllib.request.Request(
    f"http://127.0.0.1:7830/api/assumptions/{target['id']}/refute", method="POST"
)
result = json.load(urllib.request.urlopen(req))
print("证伪:", result["statement"])
print("失败测试:", result["refutation"]["test_path"])
print("测试名:", result["refutation"]["test_name"])
EOF

# ---- 台账速览：只打印每条记录的 statement ----
cat > "$CTL/statements.py" <<'EOF'
import json
import sys

for line in open(sys.argv[1], encoding="utf-8"):
    print(" -", json.loads(line)["statement"])
EOF

echo "DEMO=$DEMO"
echo "CTL=$CTL"
echo "演示仓库与控制文件就绪。"
