// Package skillhub 提供「Skill 广场」的数据来源：
//   - builtin.go：官方内置办公 skill 目录（静态、开箱即用）
//   - github.go：从 GitHub 搜索办公类仓库并映射为可安装 Manifest（带缓存 + 降级）
//
// 本包只产出声明式的 skill.Manifest，不下载 / 执行任何外部代码。
package skillhub

import (
	"agi-assistant/internal/domain/skill"
	"agi-assistant/internal/domain/tool"
)

// inputParam 是 Prompt 驱动 skill 的统一入参（自然语言输入）。
func inputParam(desc string) []tool.Param {
	return []tool.Param{{Name: "input", Type: "string", Description: desc, Required: true}}
}

// BuiltinOfficeSkills 返回官方内置的办公场景 skill 目录。
// 这些 skill 全部为 Prompt 驱动：安装并开启后由本地 LLM 按模板执行。
func BuiltinOfficeSkills() []skill.Manifest {
	return []skill.Manifest{
		{
			ID:          "builtin:meeting_minutes",
			Name:        "会议纪要生成",
			Description: "把零散的会议记录整理成结构化纪要：议题、结论、待办事项与负责人。",
			Category:    "office",
			Source:      skill.SourceBuiltin,
			Invocation:  skill.InvokePrompt,
			Parameters:  inputParam("原始会议记录 / 讨论内容"),
			PromptTemplate: "你是资深会议纪要助理。请把下面的会议内容整理为结构化纪要，包含：\n" +
				"1) 会议主题；2) 关键讨论与结论；3) 待办事项（含负责人与截止时间，若缺失则标注 待定）。\n\n会议内容：\n{{input}}",
		},
		{
			ID:          "builtin:email_draft",
			Name:        "邮件起草",
			Description: "根据要点快速起草专业、得体的中/英文商务邮件。",
			Category:    "office",
			Source:      skill.SourceBuiltin,
			Invocation:  skill.InvokePrompt,
			Parameters:  inputParam("邮件要点 / 目的 / 收件人背景"),
			PromptTemplate: "你是专业商务沟通助理。请根据以下要点起草一封结构清晰、语气得体的邮件，" +
				"包含称呼、正文与结尾署名占位。若要点里指明了语言则用该语言，否则用中文。\n\n要点：\n{{input}}",
		},
		{
			ID:          "builtin:weekly_report",
			Name:        "周报生成",
			Description: "把碎片化的工作记录汇总成条理清晰的周报（本周完成 / 进行中 / 下周计划）。",
			Category:    "office",
			Source:      skill.SourceBuiltin,
			Invocation:  skill.InvokePrompt,
			Parameters:  inputParam("本周工作流水 / 要点"),
			PromptTemplate: "你是高效的职场周报助手。请把以下工作记录汇总为周报，分三部分：" +
				"【本周完成】【进行中/风险】【下周计划】，每条简洁、可量化。\n\n工作记录：\n{{input}}",
		},
		{
			ID:          "builtin:doc_polish",
			Name:        "公文/文档润色",
			Description: "对文稿进行语言润色、逻辑梳理与正式度调整，保留原意。",
			Category:    "office",
			Source:      skill.SourceBuiltin,
			Invocation:  skill.InvokePrompt,
			Parameters:  inputParam("待润色的原文"),
			PromptTemplate: "你是资深文字编辑。请在保留原意的前提下润色下面的文稿：" +
				"优化措辞、理顺逻辑、统一正式书面语气，并指出明显的事实/表述问题。\n\n原文：\n{{input}}",
		},
		{
			ID:          "builtin:translate",
			Name:        "中英互译",
			Description: "在中文与英文之间做地道、专业的双向翻译。",
			Category:    "office",
			Source:      skill.SourceBuiltin,
			Invocation:  skill.InvokePrompt,
			Parameters:  inputParam("需要翻译的文本"),
			PromptTemplate: "你是专业双语译员。请自动判断下面文本的语言：中文则译为英文，英文则译为中文，" +
				"追求地道、准确、符合语境，专有名词保留原文并在括号内给出译名。\n\n文本：\n{{input}}",
		},
		{
			ID:          "builtin:ppt_outline",
			Name:        "PPT 大纲生成",
			Description: "根据主题生成层次清晰的演示文稿大纲与每页要点。",
			Category:    "office",
			Source:      skill.SourceBuiltin,
			Invocation:  skill.InvokePrompt,
			Parameters:  inputParam("演示主题 / 目标受众 / 时长"),
			PromptTemplate: "你是演示设计顾问。请根据下面的需求生成 PPT 大纲：给出封面、目录、" +
				"每个章节的页标题与 3-5 条要点，以及结尾行动号召。\n\n需求：\n{{input}}",
		},
		{
			ID:          "builtin:excel_formula",
			Name:        "Excel 公式助手",
			Description: "把自然语言需求转换为 Excel/WPS 公式，并解释用法。",
			Category:    "office",
			Source:      skill.SourceBuiltin,
			Invocation:  skill.InvokePrompt,
			Parameters:  inputParam("表格计算需求（描述数据布局与目标）"),
			PromptTemplate: "你是电子表格专家。请把下面的需求转换为可直接使用的 Excel 公式，" +
				"说明每个参数含义与适用版本（如需数组公式请注明），并给出一个示例。\n\n需求：\n{{input}}",
		},
	}
}
