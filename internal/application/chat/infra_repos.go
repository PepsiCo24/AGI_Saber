// repos.go — 数据访问层 + 事件总线 + 基础设施健康状态的聚合容器。
//
// 设计：把 6 个独立 repo 接口（chat / pref / snap / ltm / ragChunk / events）
// 加 infraStatus 装进一个 struct，agent 上不再铺 7 个字段；调用点通过
// a.repos.chat.xxx 之类访问，意图比裸字段更直白。
package chat

import (
	"agi-assistant/internal/infrastructure/eventbus"
	"agi-assistant/internal/infrastructure/persistence/chathistory"
	docrepo "agi-assistant/internal/infrastructure/persistence/documentrepo"
	ltmrepo "agi-assistant/internal/infrastructure/persistence/longterm"
	"agi-assistant/internal/infrastructure/persistence/memorytx"
	prefrepo "agi-assistant/internal/infrastructure/persistence/preference"
	"agi-assistant/internal/infrastructure/persistence/ragchunk"
	"agi-assistant/internal/infrastructure/persistence/snapshot"
)

// repoBundle 聚合所有数据访问层接口与基础设施状态
type repoBundle struct {
	chat     chathistory.Repo
	pref     prefrepo.Repo
	snap     snapshot.Repo
	ltm      ltmrepo.Repo
	memoryTx memorytx.Store
	ragChunk ragchunk.Repo
	docs     docrepo.Repo
	events   eventbus.Publisher
	infra    map[string]string // platform 层连接健康快照
}

// newRepoBundle 从启动时的 Deps 容器组装 repoBundle
func newRepoBundle(deps Deps) *repoBundle {
	return &repoBundle{
		chat:     deps.ChatRepo,
		pref:     deps.PrefRepo,
		snap:     deps.SnapRepo,
		ltm:      deps.LTMRepo,
		memoryTx: deps.MemoryTxRepo,
		ragChunk: deps.RAGChunkRepo,
		docs:     deps.DocumentRepo,
		events:   deps.Events,
		infra:    deps.InfraStatus,
	}
}

// infraSnapshot 返回 infraStatus 的拷贝，避免外部修改影响 agent 内部状态
func (r *repoBundle) infraSnapshot() map[string]string {
	out := make(map[string]string, len(r.infra))
	for k, v := range r.infra {
		out[k] = v
	}
	return out
}
