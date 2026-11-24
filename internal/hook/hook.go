// Package hook 适配 Claude Code 的 Hook 协议: 解析宿主写入 stdin 的 JSON, 把 Pairwise 的回应渲染成宿主读取的 stdout JSON.
// 只做协议转换, 不含业务判断.
package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
)

// pair hook 的事件, 插件把宿主事件挂到对应的子命令上.
const (
	// SessionStart 同时承接宿主的 SessionStart 与 CwdChanged.
	SessionStart = "session-start"
	PromptSubmit = "prompt-submit"
	BeforeTool   = "before-tool"
	AfterTool    = "after-tool"
	BeforeStop   = "before-stop"
	SessionEnd   = "session-end"
)

// Events 是 pair hook 接受的全部事件.
var Events = []string{SessionStart, PromptSubmit, BeforeTool, AfterTool, BeforeStop, SessionEnd}

// hostEvents 是各事件在 hookSpecificOutput.hookEventName 中的宿主事件名. session-start 不输出,
// 因为承接的 CwdChanged 不支持附加上下文; after-tool 与 session-end 只记录状态, 也不输出.
var hostEvents = map[string]string{
	PromptSubmit: "UserPromptSubmit",
	BeforeTool:   "PreToolUse",
}

// sessionIDRe 限定会话 ID 的字符: 它会以 export 语句写入会话环境文件, 每条 Bash 命令执行前都会执行该文件.
var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9._:@%+=,/-]+$`)

// Input 是宿主 Hook 输入中 Pairwise 用到的字段.
type Input struct {
	SessionID string `json:"session_id"`
	// Cwd 是宿主调用 Hook 时的工作目录.
	Cwd      string `json:"cwd"`
	ToolName string `json:"tool_name"`
	// ToolInput 的结构随工具而定: before-tool 收到全部工具的调用, MCP 工具的同名字段可能不是字符串, 因此按工具取用时再解码.
	ToolInput json.RawMessage `json:"tool_input"`
	// ToolResponse 的结构随工具而定, 调用失败时是错误文本字符串.
	ToolResponse json.RawMessage `json:"tool_response"`
}

// Parse 解析宿主写入 stdin 的 JSON.
func Parse(payload []byte) (*Input, error) {
	in := &Input{}
	if err := json.Unmarshal(payload, in); err != nil {
		return nil, fmt.Errorf("宿主 Hook 输入不是合法的 JSON: %w", err)
	}
	if !sessionIDRe.MatchString(in.SessionID) {
		return nil, fmt.Errorf("宿主 Hook 输入的 session_id %q 为空或含有不允许的字符", in.SessionID)
	}
	return in, nil
}

// EditPath 返回 Edit / Write / MultiEdit / NotebookEdit 的目标路径, 其他工具返回空字符串.
func (in *Input) EditPath() string {
	switch in.ToolName {
	case "Edit", "Write", "MultiEdit":
		return in.toolInput("file_path")
	case "NotebookEdit":
		return in.toolInput("notebook_path")
	}
	return ""
}

// Command 返回 Bash 工具的命令, 其他工具返回空字符串.
func (in *Input) Command() string {
	if in.ToolName != "Bash" {
		return ""
	}
	return in.toolInput("command")
}

// toolInput 返回 tool_input 中的字符串字段, 字段不存在或不是字符串时返回空字符串.
func (in *Input) toolInput(key string) string {
	var v map[string]any
	if json.Unmarshal(in.ToolInput, &v) != nil {
		return ""
	}
	s, _ := v[key].(string)
	return s
}

// Stdout 返回 Bash 工具的标准输出; 调用失败时宿主给出的是错误文本, 同样返回.
func (in *Input) Stdout() string {
	if in.ToolName != "Bash" {
		return ""
	}
	var res struct {
		Stdout string `json:"stdout"`
	}
	if json.Unmarshal(in.ToolResponse, &res) == nil {
		return res.Stdout
	}
	var text string
	_ = json.Unmarshal(in.ToolResponse, &text)
	return text
}

// Output 是 Pairwise 对一次 Hook 的回应, 零值表示不输出.
type Output struct {
	// Context 是附加到模型上下文的提示.
	Context string
	// Deny 是拒绝本次工具调用的原因, 只用于 before-tool.
	Deny string
	// Block 是阻止本回合结束的原因, 只用于 before-stop.
	Block string
}

type specific struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
	AdditionalContext        string `json:"additionalContext,omitempty"`
}

type output struct {
	Decision           string    `json:"decision,omitempty"`
	Reason             string    `json:"reason,omitempty"`
	HookSpecificOutput *specific `json:"hookSpecificOutput,omitempty"`
}

// Render 把回应渲染成 event 对应的宿主 stdout JSON, 没有内容时返回 nil. Block 用于 before-stop, Deny 用于 before-tool.
// 拒绝只给出 deny 而不给 allow, 附加上下文不带权限决定, 都不绕过宿主的权限确认.
func Render(event string, o Output) ([]byte, error) {
	var out output
	switch {
	case o.Block != "":
		out.Decision, out.Reason = "block", o.Block
	case o.Deny != "":
		out.HookSpecificOutput = &specific{HookEventName: hostEvents[event], PermissionDecision: "deny", PermissionDecisionReason: o.Deny}
	case o.Context != "":
		out.HookSpecificOutput = &specific{HookEventName: hostEvents[event], AdditionalContext: o.Context}
	default:
		return nil, nil
	}
	if out.HookSpecificOutput != nil && out.HookSpecificOutput.HookEventName == "" {
		return nil, fmt.Errorf("%s 不支持该输出", event)
	}
	// 提示中的 `<hash>` 等占位符按原样输出, 便于阅读.
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	err := enc.Encode(out)
	return b.Bytes(), err
}
