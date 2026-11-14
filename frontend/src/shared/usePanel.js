import { useEffect, useMemo, useRef } from 'react'
import { App } from 'antd'
import { useTranslation } from 'react-i18next'
import { useNodeController } from '../core/useNodeController'

export function useFeedback() {
  const { message, modal } = App.useApp()
  const pending = useRef(new Set())
  // 面板卸载（切换设计、退出登录）时关闭仍然打开的确认框，并按「取消」处理，
  // 避免用户在已经离开的面板上继续确认删除等危险操作。
  useEffect(() => {
    const entries = pending.current
    return () => {
      for (const entry of entries) {
        entry.instance.destroy()
        entry.resolve(false)
      }
      entries.clear()
    }
  }, [])
  return useMemo(
    () => ({
      notify: {
        success: (content) => message.success(content),
        error: (content) => message.error(content),
        warning: (content) => message.warning(content),
        info: (content) => message.info(content),
      },
      confirm: ({ title, content, okText, danger = false }) =>
        new Promise((resolve) => {
          const entry = { resolve }
          const settle = (value) => {
            pending.current.delete(entry)
            resolve(value)
          }
          entry.instance = modal.confirm({
            title,
            content,
            okText,
            centered: true,
            autoFocusButton: danger ? 'cancel' : 'ok',
            okButtonProps: { danger },
            onOk: () => settle(true),
            onCancel: () => settle(false),
          })
          pending.current.add(entry)
        }),
    }),
    [message, modal]
  )
}

export function usePanel(session) {
  const { t, i18n } = useTranslation()
  const { notify, confirm } = useFeedback()
  const ctl = useNodeController({
    t,
    notify,
    confirm,
    onPasswordChanged: () => session.logout({ revoke: false }),
  })
  // act 的引用永不变化，但每次调用都会转发到最新的控制器方法；
  // 把它传给 memo 过的列表行，行就不会因为控制器对象重建而重绘。
  const ctlRef = useRef(ctl)
  useEffect(() => {
    ctlRef.current = ctl
  })
  const act = useMemo(
    () =>
      new Proxy({}, {
        // 只转发具名方法；Symbol 与 then 返回 undefined，避免被误判为 Promise 或被调试工具探测时出错。
        get: (_, key) => (typeof key !== 'string' || key === 'then' ? undefined : (...args) => ctlRef.current[key](...args)),
      }),
    []
  )
  return { ctl, act, t, i18n, notify, confirm }
}
