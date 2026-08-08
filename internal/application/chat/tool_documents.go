package chat

import "encoding/json"

// jsonString 把任意值序列化为缩进 JSON 字符串（doc_agent 等子 Agent 复用）。
//
// 说明：本次裁剪已下线 write_document / list_documents / read_document /
// ingest_document 这几个对话工具（改由 doc_agent 直接调用 a.WriteDocument）。
// 文件仅保留此通用辅助函数。
func jsonString(v interface{}) string {
	data, _ := json.MarshalIndent(v, "", "  ")
	return string(data)
}
