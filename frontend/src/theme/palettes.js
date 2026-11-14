// 暮色单一主题令牌：CSS 与组件库共用。
const FONT_CJK = '"PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", "Noto Sans SC", "Source Han Sans SC", sans-serif'
const FONT_MONO = '"JetBrains Mono Variable", "SFMono-Regular", Menlo, Consolas, monospace'

export const THEMES = {
  dusk: {
    id: 'dusk',
    dark: true,
    name: { zh: '暮色 · 暗色指挥台', en: 'Dusk · Command Center' },
    tagline: {
      zh: '暗色莫兰迪 + 高密度列表 + Ctrl/⌘ K 命令面板，适合键盘党与夜间使用',
      en: 'Dark Morandi, dense rows and a Ctrl/⌘ K palette for keyboard-first use.',
    },
    swatches: ['#A6ACD0', '#6E7391', '#353A4D', '#22252A', '#D6B983'],
    fontUI: `"Inter Variable", ${FONT_CJK}`,
    fontDisplay: `"Inter Variable", ${FONT_CJK}`,
    radius: 8,
    colors: {
      bg: '#1E2125',
      bg2: '#24272C',
      surface: '#292C32',
      surface2: '#31353C',
      line: '#393E46',
      lineStrong: '#4C525C',
      ink: '#E8E4DC',
      ink2: '#BDB9B1',
      ink3: '#A19E97',
      accent: '#A6ACD0',
      accentStrong: '#BAC0DE',
      accentSoft: '#353A4D',
      accentInk: '#1E2125',
      muted: '#79808B',
      ok: '#7E9B85',
      okSoft: '#2B3A32',
      okInk: '#AECBB6',
      warn: '#D5B882',
      warnSoft: '#3E3627',
      warnInk: '#E0C794',
      bad: '#C68E85',
      badSoft: '#42302C',
      badInk: '#E6B2A8',
    },
    shadow1: '0 1px 2px rgba(0, 0, 0, 0.28)',
    shadow2: '0 18px 44px rgba(0, 0, 0, 0.42), 0 2px 8px rgba(0, 0, 0, 0.28)',
  },
}

export const FONT_MONO_STACK = FONT_MONO

const luminance = (hex) => {
  const value = Number.parseInt(hex.slice(1), 16)
  const channel = (shift) => {
    const c = ((value >> shift) & 255) / 255
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * channel(16) + 0.7152 * channel(8) + 0.0722 * channel(0)
}
const ratio = (a, b) => (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)
export const readableOn = (hex) => {
  const l = luminance(hex)
  return ratio(l, 1) >= ratio(l, luminance('#0B0B0B')) ? '#FFFFFF' : '#0B0B0B'
}

const kebab = (key) => key.replace(/[A-Z0-9]/g, (ch) => `-${ch.toLowerCase()}`)

// 生成写到 :root 上的 CSS 变量（--bg、--accent-soft、--ink-2 …）。
export const themeToCssVars = (theme) => {
  const vars = {}
  for (const [key, value] of Object.entries(theme.colors)) {
    vars[`--${kebab(key)}`] = value
  }
  // 状态色块上的文字颜色：白或近黑，取对比度更高的一个。
  for (const key of ['ok', 'warn', 'bad', 'muted']) {
    vars[`--${key}-on`] = readableOn(theme.colors[key])
  }
  vars['--radius'] = `${theme.radius}px`
  vars['--radius-sm'] = `${Math.max(4, theme.radius - 4)}px`
  vars['--radius-lg'] = `${theme.radius + 6}px`
  vars['--shadow-1'] = theme.shadow1
  vars['--shadow-2'] = theme.shadow2
  vars['--font-ui'] = theme.fontUI
  vars['--font-display'] = theme.fontDisplay
  vars['--font-mono'] = FONT_MONO
  return vars
}

// 把同一份令牌映射为 Ant Design 的种子/别名 token，保证弹窗、表单等组件与页面同色系。
export const themeToAntdTokens = (theme) => {
  const c = theme.colors
  return {
    token: {
      colorPrimary: c.accent,
      colorInfo: c.accent,
      colorSuccess: c.ok,
      colorWarning: c.warn,
      colorError: c.bad,
      colorLink: c.accent,
      colorTextBase: c.ink,
      colorText: c.ink,
      colorTextSecondary: c.ink2,
      colorTextTertiary: c.ink3,
      colorTextDescription: c.ink3,
      colorTextPlaceholder: c.ink3,
      colorBgBase: c.surface,
      colorBgLayout: c.bg,
      colorBgContainer: c.surface,
      colorBgElevated: theme.dark ? c.surface2 : c.surface,
      colorBorder: c.lineStrong,
      colorBorderSecondary: c.line,
      colorSplit: c.line,
      colorFillTertiary: c.surface2,
      colorFillSecondary: c.bg2,
      borderRadius: theme.radius,
      borderRadiusLG: theme.radius + 4,
      borderRadiusSM: Math.max(4, theme.radius - 4),
      fontFamily: theme.fontUI,
      fontFamilyCode: FONT_MONO,
      fontSize: 14,
      controlHeight: 36,
      controlHeightLG: 44,
      controlHeightSM: 28,
      boxShadow: theme.shadow2,
      boxShadowSecondary: theme.shadow2,
      motionDurationMid: '0.18s',
      wireframe: false,
    },
    components: {
      Button: {
        primaryColor: c.accentInk,
        colorPrimaryHover: c.accentStrong,
        colorPrimaryActive: c.accentStrong,
        fontWeight: 600,
        primaryShadow: 'none',
        defaultShadow: 'none',
        dangerShadow: 'none',
        dangerColor: theme.dark ? c.bg : '#FFFFFF',
        colorError: theme.dark ? c.bad : c.badInk,
        colorErrorHover: theme.dark ? c.badInk : c.bad,
        defaultBg: c.surface,
        defaultBorderColor: c.lineStrong,
        defaultColor: c.ink,
      },
      Switch: { colorPrimary: c.accent, colorPrimaryHover: c.accentStrong, handleBg: theme.dark ? c.bg : '#FFFFFF', colorTextQuaternary: c.muted, colorTextTertiary: c.ink3 },
      Modal: { contentBg: c.surface, headerBg: c.surface, footerBg: c.surface, titleFontSize: 17 },
      Drawer: { colorBgElevated: c.surface },
      Input: { activeShadow: `0 0 0 3px ${c.accentSoft}`, colorBgContainer: theme.dark ? c.bg2 : '#FFFFFF' },
      InputNumber: { activeShadow: `0 0 0 3px ${c.accentSoft}`, colorBgContainer: theme.dark ? c.bg2 : '#FFFFFF' },
      Select: { colorBgContainer: theme.dark ? c.bg2 : '#FFFFFF', optionSelectedBg: c.accentSoft },
      Segmented: { trackBg: c.bg2, itemSelectedBg: c.surface, itemSelectedColor: c.ink },
      Tooltip: { colorBgSpotlight: theme.dark ? '#0F1113' : c.ink, colorTextLightSolid: theme.dark ? c.ink : '#FFFFFF' },
      Dropdown: { controlItemBgHover: c.surface2 },
      Checkbox: { colorPrimary: c.accent, colorPrimaryHover: c.accentStrong },
      Tabs: { itemSelectedColor: c.ink, itemColor: c.ink3, inkBarColor: c.accent, itemHoverColor: c.ink },
      Alert: { colorErrorBg: c.badSoft, colorErrorBorder: 'transparent', colorWarningBg: c.warnSoft, colorWarningBorder: 'transparent', colorInfoBg: c.accentSoft, colorInfoBorder: 'transparent', colorSuccessBg: c.okSoft, colorSuccessBorder: 'transparent' },
    },
  }
}
