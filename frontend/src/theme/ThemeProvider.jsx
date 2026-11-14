import { useLayoutEffect, useMemo } from 'react'
import { App as AntApp, ConfigProvider, theme as antdTheme } from 'antd'
import enUS from 'antd/locale/en_US'
import zhCN from 'antd/locale/zh_CN'
import { useTranslation } from 'react-i18next'
import { THEMES, themeToAntdTokens, themeToCssVars } from './palettes'

export function ThemeProvider({ children }) {
  const { i18n } = useTranslation()
  const theme = THEMES.dusk
  useLayoutEffect(() => {
    const root = document.documentElement
    const vars = themeToCssVars(theme)
    for (const [name, value] of Object.entries(vars)) root.style.setProperty(name, value)
    root.dataset.theme = theme.id
    root.style.colorScheme = 'dark'
    return () => {
      for (const name of Object.keys(vars)) root.style.removeProperty(name)
    }
  }, [theme])
  const config = useMemo(() => ({ algorithm: antdTheme.darkAlgorithm, ...themeToAntdTokens(theme) }), [theme])
  return (
    <ConfigProvider theme={config} locale={i18n.language?.startsWith('zh') ? zhCN : enUS}>
      <AntApp className="sb-app" message={{ maxCount: 3, top: 'calc(var(--chrome-h) + 12px)' }}>
        {children}
      </AntApp>
    </ConfigProvider>
  )
}
