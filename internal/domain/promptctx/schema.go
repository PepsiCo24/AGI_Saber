package promptctx

// RuntimeContextSchema 定义某个 Mode 下需要装配的认知槽位与顺序。
// 槽位顺序即 Render 时的输出顺序。
type RuntimeContextSchema struct {
	Mode  string
	Slots []Slot
}

// 全局总预算（字符数；约等于 token 上限的 4 倍）
const defaultGlobalTokenBudget = 2400

// ChatSchema 普通对话：偏好 + 兜底召回；不需要 Planner / TaskMem / ToolState
var ChatSchema = RuntimeContextSchema{
	Mode: "chat",
	Slots: []Slot{
		{
			Kind:     SlotConstraints,
			Required: false,
			Filter:   SlotFilter{TokenBudget: 200},
		},
		{
			Kind:     SlotProfile,
			Required: false,
			Filter: SlotFilter{
				Categories:  []string{"identity", "preference"},
				TokenBudget: 300,
				TopK:        10,
			},
		},
		{
			Kind:     SlotRecall,
			Required: false,
			Filter: SlotFilter{
				Categories:  []string{"episodic", "fact", "general"},
				TopK:        3,
				MinScore:    0.4,
				TokenBudget: 400,
			},
		},
	},
}

// ToolSchema 单工具调用：弱化 Recall，强化 Tool State；不需要 Planner / TaskMem
var ToolSchema = RuntimeContextSchema{
	Mode: "tool",
	Slots: []Slot{
		{
			Kind:     SlotConstraints,
			Required: false,
			Filter:   SlotFilter{TokenBudget: 200},
		},
		{
			Kind:     SlotProfile,
			Required: false,
			Filter: SlotFilter{
				Categories:  []string{"identity", "preference"},
				TokenBudget: 250,
				TopK:        8,
			},
		},
		{
			Kind:     SlotToolState,
			Required: true,
			Filter:   SlotFilter{TokenBudget: 350, TopK: 6},
		},
		{
			Kind:     SlotRecall,
			Required: false,
			Filter: SlotFilter{
				Categories:  []string{"episodic", "fact", "general"},
				TopK:        2,
				MinScore:    0.5,
				TokenBudget: 250,
			},
		},
	},
}

// ReactSchema 多步推理：装配全部 5 类槽位
var ReactSchema = RuntimeContextSchema{
	Mode: "react",
	Slots: []Slot{
		{
			Kind:     SlotConstraints,
			Required: true,
			Filter:   SlotFilter{TokenBudget: 280},
		},
		{
			Kind:     SlotPlanner,
			Required: true,
			Filter:   SlotFilter{TokenBudget: 300},
		},
		{
			Kind:     SlotTaskMem,
			Required: false,
			Filter:   SlotFilter{TokenBudget: 350, TopK: 8, MaxAgeHours: 24},
		},
		{
			Kind:     SlotToolState,
			Required: true,
			Filter:   SlotFilter{TokenBudget: 350, TopK: 8},
		},
		{
			Kind:     SlotProfile,
			Required: false,
			Filter: SlotFilter{
				Categories:  []string{"identity", "preference"},
				TokenBudget: 250,
				TopK:        6,
			},
		},
		{
			Kind:     SlotRecall,
			Required: false,
			Filter: SlotFilter{
				Categories:  []string{"episodic", "fact", "general", "tool_failure"},
				TopK:        2,
				MinScore:    0.5,
				TokenBudget: 200,
			},
		},
	},
}

// RagSchema 知识库检索：弱化 Planner/TaskMem，保留 Profile/Constraints/Recall
var RagSchema = RuntimeContextSchema{
	Mode: "rag",
	Slots: []Slot{
		{
			Kind:     SlotConstraints,
			Required: false,
			Filter:   SlotFilter{TokenBudget: 200},
		},
		{
			Kind:     SlotProfile,
			Required: false,
			Filter: SlotFilter{
				Categories:  []string{"identity", "preference"},
				TokenBudget: 300,
				TopK:        8,
			},
		},
		{
			Kind:     SlotRecall,
			Required: false,
			Filter: SlotFilter{
				Categories:  []string{"episodic", "fact", "general"},
				TopK:        3,
				MinScore:    0.4,
				TokenBudget: 400,
			},
		},
	},
}

// DefaultSchemas 返回 4 个内置 Schema，按 Mode 字符串索引
func DefaultSchemas() map[string]RuntimeContextSchema {
	return map[string]RuntimeContextSchema{
		"chat":  ChatSchema,
		"tool":  ToolSchema,
		"react": ReactSchema,
		"rag":   RagSchema,
	}
}

// slotPriority 决定全局预算超限时的裁剪优先级（数字越小越优先保留）
func slotPriority(kind SlotKind) int {
	switch kind {
	case SlotConstraints:
		return 0
	case SlotPlanner:
		return 1
	case SlotTaskMem:
		return 2
	case SlotToolState:
		return 3
	case SlotProfile:
		return 4
	case SlotRecall:
		return 5
	}
	return 99
}
