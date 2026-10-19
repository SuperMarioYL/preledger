# examples

三行看懂 Preledger 的样子（假设已经在台账里的评审者视角）：

```text
$ preledger up            # 打开 http://127.0.0.1:7830
  待定  config.yaml 存在且含 server.port        依据：「我会假定 config.yaml 存在且含 server.port」
  待定  .env 存在且含 API_TOKEN                依据：「密钥我假定仓库里已经配置了 API_TOKEN」
$ # 点「证伪」→ 仓库里出现 preledger_refute_<id>_test.go，go test 变红；修复后转绿
```

可复现的完整演示见 [`demo/`](./demo/)：`setup.sh` 现场生成一个最小 Go 仓库、三段与 Claude Code hook stdin 同构的载荷和一段真实转录摘录，按 README 快速开始跑完 捕获 → 评审 → 证伪 → 红转绿 的闭环。
