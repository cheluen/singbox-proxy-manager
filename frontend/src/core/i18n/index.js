import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'
import baseZh from './base-zh'
import baseEn from './base-en'
import extraZh from './extra-zh'
import extraEn from './extra-en'

export const LANGUAGE_STORAGE_KEY = 'language'
export const LANGUAGES = [
  { value: 'zh', label: '中文', short: '中' },
  { value: 'en', label: 'English', short: 'EN' },
]

const readStoredLanguage = () => {
  try {
    return window.localStorage.getItem(LANGUAGE_STORAGE_KEY) || 'zh'
  } catch {
    return 'zh'
  }
}

i18n.use(initReactI18next).init({
  resources: {
    // 基础文案与面板专用词条合并；两套字典都只保留当前界面使用的词条。
    zh: { translation: { ...baseZh, ...extraZh } },
    en: { translation: { ...baseEn, ...extraEn } },
  },
  lng: readStoredLanguage(),
  fallbackLng: 'zh',
  interpolation: { escapeValue: false },
})

const syncDocumentLang = (lang) => {
  document.documentElement.lang = String(lang).startsWith('zh') ? 'zh-CN' : 'en'
}
syncDocumentLang(i18n.language)

export const changeLanguage = (lang) => {
  i18n.changeLanguage(lang)
  try {
    window.localStorage.setItem(LANGUAGE_STORAGE_KEY, lang)
  } catch {
    // 存储不可用时仅本次会话生效
  }
  syncDocumentLang(lang)
}

export default i18n
