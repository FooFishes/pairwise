#!/bin/bash
# 用真实的 claude -p 会话验收 Claude Code 插件的 Hook: 会话身份注入、拦截直接编辑 .pair/、scope 绑定提示、
# 回合结束前拦截与转向反馈. 需要 claude 已登录 (claude auth login) 与 sqlite3, 会消耗少量 haiku 用量.
set -u
SRC=$(cd "$(dirname "$0")/.." && pwd)
A=$(mktemp -d /tmp/pair-accept-XXXX)
mkdir -p "$A/bin" "$A/rt"
(cd "$SRC" && go build -o "$A/bin/pair" ./cmd/pair) || exit 1
export PATH="$A/bin:$PATH" PAIR_RUNTIME_DIR="$A/rt"
# 在 Claude Code 会话中运行本脚本时, 不让子会话继承外层会话的身份.
unset CLAUDECODE CLAUDE_CODE_ENTRYPOINT CLAUDE_CODE_SESSION_ID CLAUDE_CODE_HOST_SESSION_ID CLAUDE_CODE_CHILD_SESSION

R=$A/repo
mkdir -p "$R/src/discover" && cd "$R" || exit 1
git init -q -b main && git config user.name demo && git config user.email demo@example.com
pair init --git-hooks >/dev/null
cat > "$A/discover.md" <<'DOC'
---
id: discover
scope:
  - "src/discover/**"
---

# 发现页

## 规则 <!-- #rules -->

列表按距离 (distance) 从近到远排序.
DOC
pair update discover --kind module --file "$A/discover.md" >/dev/null
echo 'package discover' > src/discover/sort.go
git add -A && git commit -q -m init

fail=0
check() { if eval "$2"; then echo "ok   $1"; else echo "FAIL $1"; fail=1; fi; }
run() {
  claude -p "$2" --plugin-dir "$SRC/plugin/claude-code" --model haiku --permission-mode acceptEdits \
    --allowedTools "Bash(echo:*)" "Bash(ls:*)" "Bash(pair:*)" --output-format stream-json --verbose > "$A/$1.jsonl" 2> "$A/$1.err"
}
q() { sqlite3 "$R/.git/pair/pair.db" "$1"; }
latest() { q "select id from sessions order by updated_at desc limit 1"; }

run run1 'Do these steps exactly, in order. Do not skip any step and do not work around a denied tool call.
1. Run this Bash command: echo "SID=$PAIR_SESSION_ID"
2. Use the Edit tool on .pair/modules/discover.md to replace the word distance with rating. Only use the Edit tool for this; if it is denied, do not try another way.
3. Use the Write tool to create src/discover/page.go with the content: package discover
Then reply with a short report.'
SID=$(latest)
check "PAIR_SESSION_ID 进入 Bash" "[ -n '$SID' ] && grep -q 'SID=$SID' '$A/run1.jsonl'"
check "Edit .pair/ 被拒绝并给出 pair show" "grep -q 'pair show discover' '$A/run1.jsonl'"
check ".pair/ 文档未被改动" "git diff --quiet -- .pair"
check "scope 源码给出 bind 提示" "grep -q 'pair bind discover' '$A/run1.jsonl'"
check "回合结束被拦截过" "q \"select data from turns where session_id = '$SID'\" | grep -q '\"Blocked\":\\[\"'"

pair stage note 'discover#rules' 改为按评分排序 >/dev/null
pair trigger --steer >/dev/null
run run2 'Run the Bash command `ls src`, then follow any instructions you receive, and reply with a short report.'
SID2=$(latest)
check "转向反馈未读时 ls 被拒绝并给出 pair drain" "grep -q 'pair drain' '$A/run2.jsonl'"
check "Agent 执行 pair drain 读取了反馈" "q \"select data from turns where session_id = '$SID2'\" | grep -q '\"Drained\":\\[\"fb-1\"'"

pair serve --stop >/dev/null
echo "会话输出在 $A"
exit $fail
