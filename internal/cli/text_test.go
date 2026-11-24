package cli

import (
	"testing"

	pairv1 "github.com/FooFishes/pairwise/gen/pair/v1"
)

func TestText(t *testing.T) {
	decision := &pairv1.Feedback{
		Id: "closed-shop-policy", Kind: pairv1.FeedbackKind_FEEDBACK_KIND_DECISION,
		Pick: "opt-a", PickTitle: "按场景分流", Targets: []string{"discover#list", "pick-shop#list"},
		Progress: []*pairv1.TargetProgress{{Target: "discover#list", Status: "updated"}},
	}
	note := &pairv1.Feedback{
		Id: "fb-3", Kind: pairv1.FeedbackKind_FEEDBACK_KIND_NOTE, Target: "discover#tabs",
		AssumptionId: "shared-sort", AssumptionText: "共用排序状态", Note: "切换分类时重置排序", Steer: true,
	}
	tests := []struct {
		name, got, want string
	}{
		{
			name: "drain 列出反馈并给出闭环命令",
			got:  drainText(&pairv1.DrainResponse{Items: []*pairv1.Feedback{decision, note}}),
			want: "已读取 2 条开发者确认反馈:\n" +
				"- [id: closed-shop-policy] 选择方案 opt-a (按场景分流)\n" +
				"  受影响目标: discover#list, pick-shop#list\n" +
				"- [id: fb-3] 修改说明 discover#tabs 假设 shared-sort [立即转向]\n" +
				"  假设内容: 共用排序状态\n" +
				"  说明: 切换分类时重置排序\n" +
				"\n" +
				"下一步要求: 请按上述结论修改业务代码, 随后逐条闭环 (base 由 `pair show <module#sec>` 获取):\n" +
				"- closed-shop-policy: 对每个受影响目标执行 `pair update <module#sec> --base <hash> --resolves closed-shop-policy` " +
				"(某目标确认无需改动时改用 `pair update <module#sec> --resolves closed-shop-policy --no-change \"<原因>\"`); " +
				"全部目标更新或显式结案后工具将自动删除 `.pair/decisions/closed-shop-policy.md`\n" +
				"- fb-3: 执行 `pair update discover#tabs --base <hash> --resolves fb-3`\n",
		},
		{
			name: "ask --wait 超时按默认方案继续",
			got:  waitText(&pairv1.WaitDecisionResponse{DefaultOption: "opt-a", TimeoutSecs: 90}),
			want: "暂未确认, 请先按 default_option opt-a 继续执行, 问题已保留在队列中 (已等待 90 秒)\n" +
				"下一步: 开发者确认并发送后执行 `pair drain` 读取结论\n",
		},
		{
			name: "update 输出新 base、闭环结果与剩余目标",
			got: updateText(&pairv1.UpdateRequest{Target: "discover#list"}, &pairv1.UpdateResponse{
				DocId: "discover", Path: ".pair/modules/discover.md", Base: "a84f21c09e12", Changed: true,
				Resolved: []string{"fb-3"}, RemovedAssumptions: []string{"shared-sort"},
				Retracted:     []*pairv1.Assumption{{Id: "no-mealtime", Text: "不传饭点参数"}},
				OpenDecisions: []*pairv1.Feedback{decision},
			}),
			want: "已更新 discover#list (.pair/modules/discover.md)\n" +
				"新 base: a84f21c09e12\n" +
				"已闭环: fb-3\n" +
				"已移除假设: shared-sort\n" +
				"已撤回假设 (未关联 --resolves 删去, 已移入 retracted 待开发者知晓):\n" +
				"- no-mealtime: 不传饭点参数\n" +
				"剩余未闭环:\n" +
				"- [id: closed-shop-policy] 剩余目标: pick-shop#list\n" +
				"下一步: 对剩余目标执行 `pair update <module#sec> --base <hash> --resolves <id>`, " +
				"无需改动的目标执行 `pair update <module#sec> --resolves <id> --no-change \"<原因>\"`\n",
		},
		{
			name: "update --no-change 完成最后一个目标后删除问题",
			got: updateText(&pairv1.UpdateRequest{Target: "pick-shop#list", NoChange: "选店页规则不变"}, &pairv1.UpdateResponse{
				DocId: "pick-shop", Path: ".pair/modules/pick-shop.md",
				Resolved: []string{"closed-shop-policy"}, DeletedDecisions: []string{"closed-shop-policy"},
			}),
			want: "已将 pick-shop#list 标记为已核对 · 无需改动: 选店页规则不变\n" +
				"已闭环: closed-shop-policy\n" +
				"问题 closed-shop-policy 的全部目标已完成, 已删除 .pair/decisions/closed-shop-policy.md\n" +
				"下一步: 继续当前任务\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("输出:\n%s\n期望:\n%s", tt.got, tt.want)
			}
		})
	}
}
