// 登录会话：令牌的保存、恢复与失效处理。
import { useCallback, useEffect, useState } from 'react'
import api from './api'

const TOKEN_KEY = 'token'
const LEGACY_TOKEN_KEY = 'sbpm-dusk:token'

const readToken = () => {
  try {
    return window.localStorage.getItem(TOKEN_KEY) || window.localStorage.getItem(LEGACY_TOKEN_KEY)
  } catch {
    return null
  }
}

const writeToken = (token) => {
  try {
    if (token) window.localStorage.setItem(TOKEN_KEY, token)
    else window.localStorage.removeItem(TOKEN_KEY)
    window.localStorage.removeItem(LEGACY_TOKEN_KEY)
  } catch {
    // 存储不可用时会话只在内存中保留
  }
}

export function useSession() {
  const [token, setToken] = useState(() => {
    const saved = readToken()
    if (saved) api.setToken(saved)
    return saved
  })

  const apply = useCallback((next) => {
    writeToken(next)
    api.setToken(next)
    setToken(next)
  }, [])

  useEffect(() => {
    const handleUnauthorized = () => apply(null)
    window.addEventListener('sbpm-dusk:unauthorized', handleUnauthorized)
    return () => window.removeEventListener('sbpm-dusk:unauthorized', handleUnauthorized)
  }, [apply])

  // 登录失败时把后端错误原样抛给调用方，由登录页决定如何展示。
  const login = useCallback(async (secret) => {
    const response = await api.post('/login', { password: secret })
    apply(response.data.token)
  }, [apply])

  const logout = useCallback(async ({ revoke = true } = {}) => {
    if (revoke) {
      try {
        await api.post('/logout')
      } catch {
        // 即使后端登出失败也清除本地会话，强制重新认证
      }
    }
    apply(null)
  }, [apply])

  return { token, login, logout }
}
