package promptctx

import (
	"context"
	"sort"
	"sync"
)

// SourceRegistry 持有按 SlotKind 分组的所有 ContextSource 注册。
type SourceRegistry struct {
	mu      sync.RWMutex
	sources map[SlotKind][]ContextSource
}

// NewSourceRegistry 创建空注册表
func NewSourceRegistry() *SourceRegistry {
	return &SourceRegistry{sources: make(map[SlotKind][]ContextSource)}
}

// Register 将 source 注册到它声明支持的所有 SlotKind
func (r *SourceRegistry) Register(source ContextSource) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, kind := range allSlotKinds {
		if source.Supports(kind) {
			r.sources[kind] = append(r.sources[kind], source)
		}
	}
}

// For 返回支持指定 SlotKind 的全部 source（只读快照）
func (r *SourceRegistry) For(kind SlotKind) []ContextSource {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := r.sources[kind]
	cp := make([]ContextSource, len(list))
	copy(cp, list)
	return cp
}

var allSlotKinds = []SlotKind{
	SlotProfile, SlotPlanner, SlotTaskMem, SlotToolState, SlotConstraints, SlotRecall,
}

// ContextAssembler 是装配入口：根据 Mode 选 Schema，并发调各 source 填充槽位。
type ContextAssembler struct {
	schemas     map[string]RuntimeContextSchema
	registry    *SourceRegistry
	globalLimit int // 全局字符预算
}

// NewAssembler 创建 ContextAssembler
func NewAssembler(schemas map[string]RuntimeContextSchema, registry *SourceRegistry) *ContextAssembler {
	if len(schemas) == 0 {
		schemas = DefaultSchemas()
	}
	return &ContextAssembler{
		schemas:     schemas,
		registry:    registry,
		globalLimit: defaultGlobalTokenBudget,
	}
}

// Assemble 构建当次推理的 RuntimeContext
func (a *ContextAssembler) Assemble(ctx context.Context, q Query) *RuntimeContext {
	schema, ok := a.schemas[q.Mode]
	if !ok {
		schema = a.schemas["chat"]
	}

	rc := &RuntimeContext{
		Schema: schema,
		Filled: make([]FilledSlot, len(schema.Slots)),
	}

	// 并发调各槽位对应的 source
	var wg sync.WaitGroup
	mu := sync.Mutex{}

	for idx, slot := range schema.Slots {
		idx, slot := idx, slot
		wg.Add(1)
		go func() {
			defer wg.Done()
			fs := a.fillSlot(ctx, slot, q)
			mu.Lock()
			rc.Filled[idx] = fs
			mu.Unlock()
		}()
	}
	wg.Wait()

	// 全局预算裁剪（高优先级槽位优先保留）
	a.applyGlobalBudget(rc)

	return rc
}

// fillSlot 调用注册到该 SlotKind 的 source，并做单槽位 budget 裁剪
func (a *ContextAssembler) fillSlot(ctx context.Context, slot Slot, q Query) FilledSlot {
	sources := a.registry.For(slot.Kind)
	if len(sources) == 0 {
		return FilledSlot{Kind: slot.Kind, Skipped: slot.Required, Reason: "no source registered"}
	}

	var all []ContextItem
	for _, src := range sources {
		items, err := src.Fetch(ctx, slot, q)
		if err != nil || ctx.Err() != nil {
			break
		}
		all = append(all, items...)
	}

	if len(all) == 0 {
		return FilledSlot{Kind: slot.Kind, Skipped: !slot.Required, Reason: "source returned empty"}
	}

	// 单槽位 token budget 裁剪（按字符数近似）
	if slot.Filter.TokenBudget > 0 {
		all = trimByBudget(all, slot.Filter.TokenBudget)
	}

	return FilledSlot{Kind: slot.Kind, Items: all}
}

// applyGlobalBudget 从低优先级槽位开始裁剪，直到总字符数在 globalLimit 以内
func (a *ContextAssembler) applyGlobalBudget(rc *RuntimeContext) {
	total := 0
	for _, fs := range rc.Filled {
		for _, item := range fs.Items {
			total += len(item.Text)
		}
	}
	if total <= a.globalLimit {
		return
	}

	// 按优先级从低到高排（SlotRecall 最低，SlotConstraints 最高），逐步裁剪
	order := make([]int, len(rc.Filled))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(x, y int) bool {
		return slotPriority(rc.Filled[order[x]].Kind) > slotPriority(rc.Filled[order[y]].Kind)
	})

	for _, idx := range order {
		if total <= a.globalLimit {
			break
		}
		fs := &rc.Filled[idx]
		for len(fs.Items) > 0 && total > a.globalLimit {
			last := fs.Items[len(fs.Items)-1]
			total -= len(last.Text)
			fs.Items = fs.Items[:len(fs.Items)-1]
		}
		if len(fs.Items) == 0 {
			fs.Skipped = !rc.Schema.Slots[idx].Required
			fs.Reason = "global budget exceeded"
		}
	}
}

// trimByBudget 按字符数裁剪 ContextItem 列表，直到总长在 budget 以内
func trimByBudget(items []ContextItem, budget int) []ContextItem {
	total := 0
	for i, item := range items {
		total += len(item.Text)
		if total > budget {
			return items[:i]
		}
	}
	return items
}
