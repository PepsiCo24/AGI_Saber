// markdown.js — 自包含 Markdown → 安全 HTML 渲染（从旧 index.html 平移）。
// 策略：先整体转义 HTML，再抽出代码块，最后逐行转 heading/list/quote/hr/table/段落 + 行内 bold/italic/code/link。

export function renderMarkdown(md) {
  if (!md) return ''
  let s = String(md).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
  // 代码块 ```lang\n...\n```
  const blocks = []
  s = s.replace(/```[^\n]*\n([\s\S]*?)```/g, (m, code) => { blocks.push(code.replace(/\n$/, '')); return `\u0000CB${blocks.length - 1}\u0000` })
  // 行内代码 `x`
  const inlines = []
  s = s.replace(/`([^`\n]+)`/g, (m, c) => { inlines.push(c); return `\u0000IC${inlines.length - 1}\u0000` })

  const inline = (t) => t
    .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
    .replace(/(^|[^*])\*([^*\n]+)\*/g, '$1<em>$2</em>')
    .replace(/\[([^\]]+)\]\((https?:[^)\s]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>')

  const lines = s.split('\n')
  let html = '', inUL = false, inOL = false
  const closeLists = () => { if (inUL) { html += '</ul>'; inUL = false } if (inOL) { html += '</ol>'; inOL = false } }
  // 表格辅助
  const splitRow = (l) => l.trim().replace(/^\|/, '').replace(/\|$/, '').split('|').map(c => c.trim())
  const isTableRow = (l) => l.indexOf('|') >= 0 && /\S/.test(l)
  const isSepRow = (l) => { const c = splitRow(l); return c.length > 0 && c.every(x => /^:?-{1,}:?$/.test(x.replace(/\s/g, ''))) }

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i].replace(/\s+$/, '')
    if (/^\s*```/.test(line)) continue
    if (/^\u0000CB\d+\u0000$/.test(line)) { closeLists(); html += line; continue }
    if (isTableRow(line) && i + 1 < lines.length && isSepRow(lines[i + 1].replace(/\s+$/, ''))) {
      closeLists()
      const header = splitRow(line)
      let tbl = '<table><thead><tr>' + header.map(c => `<th>${inline(c)}</th>`).join('') + '</tr></thead><tbody>'
      i += 2
      while (i < lines.length) {
        const r = lines[i].replace(/\s+$/, '')
        if (!isTableRow(r)) break
        tbl += '<tr>' + splitRow(r).map(c => `<td>${inline(c)}</td>`).join('') + '</tr>'
        i++
      }
      i--
      html += tbl + '</tbody></table>'
      continue
    }
    if (/^\s*(---|\*\*\*|___)\s*$/.test(line)) { closeLists(); html += '<hr>'; continue }
    const h = line.match(/^(#{1,6})\s+(.*)$/)
    if (h) { closeLists(); const l = h[1].length; html += `<h${l}>${inline(h[2])}</h${l}>`; continue }
    const bq = line.match(/^\s*>\s?(.*)$/)
    if (bq) { closeLists(); html += `<blockquote>${inline(bq[1])}</blockquote>`; continue }
    const ol = line.match(/^\s*\d+\.\s+(.*)$/)
    if (ol) { if (!inOL) { closeLists(); html += '<ol>'; inOL = true } html += `<li>${inline(ol[1])}</li>`; continue }
    const ul = line.match(/^\s*[-*+]\s+(.*)$/)
    if (ul) { if (!inUL) { closeLists(); html += '<ul>'; inUL = true } html += `<li>${inline(ul[1])}</li>`; continue }
    if (line.trim() === '') { closeLists(); continue }
    closeLists()
    html += `<p>${inline(line)}</p>`
  }
  closeLists()
  html = html.replace(/\u0000CB(\d+)\u0000/g, (m, i) => `<pre class="md-code"><code>${blocks[+i]}</code></pre>`)
  html = html.replace(/\u0000IC(\d+)\u0000/g, (m, i) => `<code class="md-inline">${inlines[+i]}</code>`)
  return html
}
