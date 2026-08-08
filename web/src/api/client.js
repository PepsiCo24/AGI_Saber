// api/client.js — 统一 fetch 封装：注入 Bearer token、401 回调、baseURL。
// 开发期 BASE 为空走 vite proxy；生产可用 VITE_API_BASE 直连后端。

const BASE = import.meta.env.VITE_API_BASE || ''

let unauthorizedHandler = null
export function setUnauthorizedHandler(fn) { unauthorizedHandler = fn }

export function getToken() { return localStorage.getItem('agi_auth_token') || '' }

export async function apiFetch(path, options = {}) {
  const headers = new Headers(options.headers || {})
  const tok = getToken()
  if (tok) headers.set('Authorization', 'Bearer ' + tok)
  options.headers = headers
  const resp = await fetch(BASE + path, options)
  if (resp.status === 401) {
    if (unauthorizedHandler) unauthorizedHandler()
    throw new Error('unauthorized')
  }
  return resp
}

// fetchJSON — 读文本再解析，兼容空 body / 非 JSON 错误体。
export async function fetchJSON(path, options) {
  const resp = await apiFetch(path, options)
  const raw = await resp.text()
  let data = {}
  if (raw) { try { data = JSON.parse(raw) } catch { data = { error: raw } } }
  if (!resp.ok) throw new Error(data.error || raw || `HTTP ${resp.status}`)
  return data
}
