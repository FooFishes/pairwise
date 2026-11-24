package hook

import "testing"

// 输入取自 Claude Code Hook 文档中的示例 (https://code.claude.com/docs/en/hooks).
func TestParse(t *testing.T) {
	for _, c := range []struct {
		name, payload         string
		path, command, stdout string
	}{
		{"PreToolUse Bash", `{"session_id":"abc123","transcript_path":"/home/user/.claude/projects/.../transcript.jsonl","cwd":"/home/user/my-project",
			"permission_mode":"default","hook_event_name":"PreToolUse","tool_name":"Bash",
			"tool_input":{"command":"npm test","description":"Run test suite","timeout":120000,"run_in_background":false},"tool_use_id":"toolu_01ABC123"}`,
			"", "npm test", ""},
		{"PostToolUse Write", `{"session_id":"abc123","cwd":"/p","hook_event_name":"PostToolUse","tool_name":"Write",
			"tool_input":{"file_path":"/path/to/file.txt","content":"file content"},"tool_response":{"filePath":"/path/to/file.txt","type":"create"}}`,
			"/path/to/file.txt", "", ""},
		{"PreToolUse NotebookEdit", `{"session_id":"abc123","cwd":"/p","tool_name":"NotebookEdit","tool_input":{"notebook_path":"/p/a.ipynb","new_source":"x"}}`,
			"/p/a.ipynb", "", ""},
		{"PostToolUse Bash", `{"session_id":"abc123","cwd":"/p","tool_name":"Bash","tool_input":{"command":"pair bind discover"},
			"tool_response":{"stdout":"bind_id: b-1\n","stderr":"","interrupted":false,"isImage":false}}`,
			"", "pair bind discover", "bind_id: b-1\n"},
		{"PostToolUse Bash 失败", `{"session_id":"abc123","cwd":"/p","tool_name":"Bash","tool_input":{"command":"false"},"tool_response":"Exit code 1"}`,
			"", "false", "Exit code 1"},
		// before-tool 收到全部工具的调用, MCP 工具的同名字段不是字符串时不能使解析失败.
		{"PreToolUse MCP", `{"session_id":"abc123","cwd":"/p","tool_name":"mcp__docker__exec","tool_input":{"command":["ls","-l"],"file_path":{"a":1}}}`,
			"", "", ""},
	} {
		in, err := Parse([]byte(c.payload))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if in.SessionID != "abc123" || in.EditPath() != c.path || in.Command() != c.command || in.Stdout() != c.stdout {
			t.Errorf("%s: session %q, path %q, command %q, stdout %q", c.name, in.SessionID, in.EditPath(), in.Command(), in.Stdout())
		}
	}
	for _, bad := range []string{`not json`, `{"cwd":"/p"}`, `{"session_id":"a; rm -rf ~"}`} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("Parse(%s) 未报错", bad)
		}
	}
}

func TestRender(t *testing.T) {
	for _, c := range []struct {
		event string
		out   Output
		want  string
	}{
		{BeforeTool, Output{Deny: "改用 `pair update <id>`"},
			`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"改用 ` + "`pair update <id>`" + `"}}` + "\n"},
		{BeforeTool, Output{Context: "先 pair bind"}, `{"hookSpecificOutput":{"hookEventName":"PreToolUse","additionalContext":"先 pair bind"}}` + "\n"},
		{PromptSubmit, Output{Context: "短码 ab3x"}, `{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"短码 ab3x"}}` + "\n"},
		{BeforeStop, Output{Block: "先 pair drain"}, `{"decision":"block","reason":"先 pair drain"}` + "\n"},
		{AfterTool, Output{}, ""},
	} {
		got, err := Render(c.event, c.out)
		if err != nil || string(got) != c.want {
			t.Errorf("Render(%s, %+v) = %s, %v; want %s", c.event, c.out, got, err, c.want)
		}
	}
	// CwdChanged 也挂在 session-start 上, 它不接受附加上下文.
	if _, err := Render(SessionStart, Output{Context: "x"}); err == nil {
		t.Error("session-start 渲染了附加上下文")
	}
}
