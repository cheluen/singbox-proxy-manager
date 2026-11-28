import { useLayoutEffect } from 'react'
import { App as AntApp, ConfigProvider, theme as antdTheme } from 'antd'
import enUS from 'antd/locale/en_US'
import zhCN from 'antd/locale/zh_CN'
import { useTranslation } from 'react-i18next'
import { THEMES, themeToAntdTokens, themeToCssVars } from './palettes'

const DUSK_THEME = THEMES.dusk
const DUSK_VARS = themeToCssVars(DUSK_THEME)
const DUSK_ANTD_CONFIG = { algorithm: antdTheme.darkAlgorithm, ...themeToAntdTokens(DUSK_THEME) }

export function ThemeProvider({ children }) {
  const { i18n } = useTranslation()
  useLayoutEffect(() => {
    const root = document.documentElement
    for (const [name, value] of Object.entries(DUSK_VARS)) root.style.setProperty(name, value)
    root.dataset.theme = DUSK_THEME.id
    root.style.colorScheme = 'dark'
    return () => {
      for (const name of Object.keys(DUSK_VARS)) root.style.removeProperty(name)
    }
  }, [])
  return (
    <ConfigProvider theme={DUSK_ANTD_CONFIG} locale={i18n.language?.startsWith('zh') ? zhCN : enUS}>
      <AntApp className="sb-app" message={{ maxCount: 3, top: 54 }}>
        {children}
      </AntApp>
    </ConfigProvider>
  )
}
