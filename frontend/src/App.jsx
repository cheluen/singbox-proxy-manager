import { useEffect } from 'react'
import { useSession } from './core/useSession'
import { PROJECT_NAME } from './core/project'
import DuskDesign from './panel'
import { TipLayer } from './shared/TipLayer'
import { ThemeProvider } from './theme/ThemeProvider'
import api from './core/api'
import { ensureLatestFrontendBuild } from './core/version'

export default function App() {
  const session = useSession()

  useEffect(() => {
    document.title = PROJECT_NAME
  }, [])

  useEffect(() => {
    // 已登录时由主面板的 useNodeManager 统一拉取 /version，避免首屏重复请求。
    if (session.token) return undefined
    const controller = new AbortController()
    api.get('/version', { signal: controller.signal })
      .then((response) => {
        if (!controller.signal.aborted) ensureLatestFrontendBuild(response.data?.version)
      })
      .catch(() => {})
    return () => controller.abort()
  }, [session.token])

  return (
    <ThemeProvider>
      <div className="dusk-stage">
        <DuskDesign session={session} />
      </div>
      <TipLayer />
    </ThemeProvider>
  )
}
