// composables/useSSE.js — 解析 SSE 流（event/data 块），逐事件回调。
export async function readSSE(resp, onEvent) {
  const reader = resp.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buf += decoder.decode(value, { stream: true })
    const parts = buf.split('\n\n')
    buf = parts.pop()
    for (const part of parts) {
      let evt = '', data = ''
      for (const line of part.split('\n')) {
        if (line.startsWith('event: ')) evt = line.slice(7).trim()
        if (line.startsWith('data: ')) data = line.slice(6)
      }
      if (!data) continue
      let parsed
      try { parsed = JSON.parse(data) } catch { continue }
      onEvent(evt, parsed)
    }
  }
  // 处理尾部残留（末尾没有 \n\n 的 done 事件）
  if (buf.trim()) {
    for (const line of buf.split('\n')) {
      if (line.startsWith('data: ')) {
        try { onEvent('done', JSON.parse(line.slice(6))) } catch { /* ignore */ }
      }
    }
  }
}
