// escape.js — HTML 转义工具（从旧 index.html 平移）

export function escHtml(s) {
  return String(s ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/\n/g, '<br>')
}

export function escAttr(s) {
  return escHtml(String(s || '')).replace(/"/g, '&quot;').replace(/'/g, '&#39;')
}
