# docs/demo.tape — vhs 录制脚本：10 分钟上手路径的实录。
#
# 录制前置：在仓库根目录运行（CI 里 demo.yml 与本地一致）：
#   vhs docs/demo.tape
# 输出：assets/demo.gif
#
# 台词对应 README 的演示流程：
#   install → 三次 hook 捕获 → 台账 → 评审界面证伪 → 红测试 → 修复转绿。

Output assets/demo.gif

Set Shell bash
Set FontSize 16
Set Width 1240
Set Height 680
Set Padding 28
Set Theme "Dracula"
Set TypingSpeed 6ms
Set CursorBlink false

Type "go build -o bin/preledger ." Enter
Sleep 400ms
Type "bash examples/demo/setup.sh /tmp/preledger-demo bin/preledger" Enter
Sleep 500ms
Type "cd /tmp/preledger-demo" Enter

Type "./preledger install" Enter
Sleep 600ms

Type "./preledger hook < /tmp/preledger-demo-files/p1.json && ./preledger hook < /tmp/preledger-demo-files/p2.json && ./preledger hook < /tmp/preledger-demo-files/p3.json" Enter
Sleep 400ms
Type "python3 /tmp/preledger-demo-files/statements.py .preledger/ledger.jsonl" Enter
Sleep 1100ms

Type "./preledger up --no-open & sleep 1" Enter
Sleep 600ms

Type "python3 /tmp/preledger-demo-files/refute.py" Enter
Sleep 900ms

Type "go test ./..." Enter
Sleep 1100ms

Type "printf 'server.port: 8080\n' > config.yaml && go test ./..." Enter
Sleep 1400ms

Type "echo 'github.com/SuperMarioYL/preledger — 让每次编辑交代它的假设'" Enter
Sleep 2000ms
