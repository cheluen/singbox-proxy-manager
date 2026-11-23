import assert from 'node:assert/strict'
import { PROJECT_MARK_FILE, PROJECT_NAME } from '../src/core/project.js'

export const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

export async function assertProjectBrand(page, { login = false } = {}) {
  await page.waitForSelector('[data-testid="project-brand"]', { visible: true })
  await page.waitForFunction(() => {
    const image = document.querySelector('.sb-logo img')
    return image?.complete && image.naturalWidth > 0
  })
  await page.evaluate(() => document.fonts.ready)
  const result = await page.evaluate(() => {
    const brand = document.querySelector('[data-testid="project-brand"]')
    const image = document.querySelector('.sb-logo img')
    const rect = (element) => {
      const bounds = element.getBoundingClientRect()
      return { left: bounds.left, right: bounds.right, top: bounds.top, bottom: bounds.bottom, width: bounds.width, height: bounds.height }
    }
    const parts = Array.from(brand.querySelectorAll('.sb-wordmark > span'), (element) => {
      const range = document.createRange()
      range.selectNodeContents(element)
      return { ...rect(element), textWidth: range.getBoundingClientRect().width, display: getComputedStyle(element).display }
    })
    const identity = document.querySelector('.dusk-top__brand')
    const loginCard = document.querySelector('.dusk-login__card')
    const loginInput = loginCard?.querySelector('[data-testid="login-secret"]')
    return {
      name: brand.textContent.replace(/\s+/g, ' ').trim(), title: document.title,
      applicationName: document.querySelector('meta[name="application-name"]')?.content,
      language: brand.lang, tag: brand.tagName, parts,
      mark: new URL(image.src).pathname, favicon: new URL(document.querySelector('link[rel="icon"]').href).pathname,
      decorative: image.alt === '' && image.closest('.sb-logo').getAttribute('aria-hidden') === 'true',
      width: innerWidth, overflow: document.documentElement.scrollWidth > innerWidth,
      identity: identity ? rect(identity) : null,
      loginCopy: loginCard ? {
        decorationPresent: Boolean(loginCard.querySelector('.dusk-login__prompt, .dusk-login__sub')),
        paragraphs: Array.from(loginCard.querySelectorAll('p'), (element) => element.textContent.trim()).filter(Boolean),
        label: loginInput?.labels?.[0]?.textContent.trim() || '',
        inputTop: loginInput?.getBoundingClientRect().top,
        headingBottom: loginCard.querySelector('.dusk-login__head').getBoundingClientRect().bottom,
      } : null,
      siblings: identity ? Array.from(identity.parentElement.children).filter((element) => element !== identity).map(rect).filter((bounds) => bounds.width > 0 && bounds.height > 0) : [],
    }
  })
  assert.equal(result.name, PROJECT_NAME)
  assert.equal(result.title, PROJECT_NAME)
  assert.equal(result.applicationName, PROJECT_NAME)
  assert.equal(result.language, 'en')
  assert.equal(result.tag, login ? 'H1' : 'STRONG')
  assert.equal(result.mark, `/${PROJECT_MARK_FILE}`)
  assert.equal(result.favicon, result.mark)
  assert.equal(result.decorative, true, '装饰标识不应重复朗读品牌名')
  assert.equal(result.overflow, false, `${result.width}px 页面横向溢出`)
  if (login) {
    assert.ok(result.loginCopy, '登录卡片必须存在')
    assert.equal(result.loginCopy.decorationPresent, false, '登录页不应残留终端路径或功能说明')
    assert.deepEqual(result.loginCopy.paragraphs, [], '未提交的登录页不应出现额外说明文字')
    assert.ok(result.loginCopy.label, '移除装饰文字后仍需保留密码输入标签')
    assert.ok(result.loginCopy.inputTop > result.loginCopy.headingBottom + 16, '品牌和登录表单之间必须保留间距')
  }
  for (const bounds of result.parts) {
    assert.ok(bounds.width > 0 && bounds.height > 0 && bounds.display !== 'none', '品牌词组不可隐藏')
    assert.ok(bounds.width + 1 >= bounds.textWidth, '品牌词组不可截断')
    assert.ok(bounds.left >= 0 && bounds.right <= result.width + 1, '完整品牌名必须在视口内')
  }
  if (result.identity) for (const bounds of result.siblings) {
    assert.ok(bounds.left >= 0 && bounds.right <= result.width + 1, '顶栏控件必须在视口内')
    const intersects = bounds.left < result.identity.right - 1 && bounds.right > result.identity.left + 1 &&
      bounds.top < result.identity.bottom - 1 && bounds.bottom > result.identity.top + 1
    assert.equal(intersects, false, '品牌与顶栏控件不可重叠')
  }
}

export async function clickText(page, text, { root = 'body', timeout = 15000, exact = true } = {}) {
  await page.waitForFunction((selector, needle, full) => {
    return Array.from(document.querySelectorAll(`${selector} button, ${selector} [role="menuitem"], ${selector} [role="option"]`)).some((element) => {
      const rect = element.getBoundingClientRect()
      const label = element.textContent.trim()
      const popup = element.closest('.ant-dropdown, .ant-select-dropdown, .ant-modal')
      const moving = popup && /-(?:appear|enter|leave)(?:-(?:active|prepare|start))?(?:\s|$)/.test(popup.className)
      return !moving && rect.width > 0 && rect.height > 0 && !element.disabled && (full ? label === needle : label.includes(needle))
    })
  }, { timeout }, root, text, exact)
  const handle = await page.evaluateHandle((selector, needle, full) => {
    return Array.from(document.querySelectorAll(`${selector} button, ${selector} [role="menuitem"], ${selector} [role="option"]`)).find((element) => {
      const rect = element.getBoundingClientRect()
      const label = element.textContent.trim()
      const popup = element.closest('.ant-dropdown, .ant-select-dropdown, .ant-modal')
      const moving = popup && /-(?:appear|enter|leave)(?:-(?:active|prepare|start))?(?:\s|$)/.test(popup.className)
      return !moving && rect.width > 0 && rect.height > 0 && !element.disabled && (full ? label === needle : label.includes(needle))
    })
  }, root, text, exact)
  const element = handle.asElement()
  assert.ok(element, `未找到控件：${text}`)
  await element.asLocator().click()
  await handle.dispose()
}

export async function setInput(page, selector, value) {
  await waitControlReady(page, selector)
  await page.$eval(selector, (element) => element.scrollIntoView({ block: 'center', inline: 'nearest' }))
  await page.locator(selector).click()
  await page.waitForFunction((target) => document.activeElement === document.querySelector(target), {}, selector)
  await page.keyboard.down('Control')
  await page.keyboard.press('a')
  await page.keyboard.up('Control')
  if (String(value)) await page.keyboard.sendCharacter(String(value))
  else await page.keyboard.press('Backspace')
  await page.waitForFunction((target, next) => document.querySelector(target)?.value === next, {}, selector, String(value))
}

async function waitControlReady(page, selector) {
  await page.bringToFront()
  await page.waitForFunction((target) => {
    const element = document.querySelector(target)
    if (!element || element.getBoundingClientRect().height === 0) return false
    const popup = element.closest('.ant-modal, .ant-popover, .ant-select-dropdown')
    return !popup || !/-(?:appear|enter|leave)(?:-(?:active|prepare|start))?(?:\s|$)/.test(popup.className)
  }, {}, selector)
}

export async function clickControl(page, selector) {
  await waitControlReady(page, selector)
  await page.locator(selector).click()
}

export async function selectOption(page, field, text) {
  const selector = `.ant-select:has(#${field}) .ant-select-selector`
  await waitControlReady(page, selector)
  await page.locator(selector).click()
  await clickText(page, text, { root: '.ant-select-dropdown:not(.ant-select-dropdown-hidden)' })
  await page.waitForFunction(() => !Array.from(document.querySelectorAll('.ant-select-dropdown')).some((element) => {
    const rect = element.getBoundingClientRect()
    return rect.height > 0 && getComputedStyle(element).display !== 'none'
  }))
}

export async function confirmDialog(page, accept = true) {
  const selector = accept ? '.ant-modal-confirm-btns .ant-btn-primary' : '.ant-modal-confirm-btns button:not(.ant-btn-primary)'
  await page.waitForSelector(selector, { visible: true })
  await clickControl(page, selector)
  await page.waitForSelector('.ant-modal-confirm-body-wrapper', { hidden: true })
}

export async function openBatchImport(page) {
  await page.locator('[data-testid="nodes-add-menu"]').click()
  await clickText(page, 'Batch Import', { root: '.ant-dropdown:not(.ant-dropdown-hidden)' })
  await page.waitForSelector('[data-testid="batch-import-content"]', { visible: true })
}

export async function openNodeMenu(page, id, action) {
  await page.locator(`[data-testid="node-menu-${id}"]`).click()
  await clickText(page, action, { root: '.ant-dropdown:not(.ant-dropdown-hidden)' })
}

export async function editNode(page, id) {
  await openNodeMenu(page, id, 'Edit')
  await page.waitForSelector('[data-testid="node-form"]', { visible: true })
}

export async function toggle(page, selector, checked) {
  await page.waitForSelector(selector, { visible: true })
  const previous = await page.$eval(selector, (element) => element.getAttribute('aria-checked') === 'true')
  if (previous !== checked) await clickControl(page, selector)
  await page.waitForFunction((target, next) => document.querySelector(target)?.getAttribute('aria-checked') === String(next), {}, selector, checked)
}

export async function saveNodeForm(page, { confirmOpenAuth = false } = {}) {
  await clickControl(page, '[data-testid="node-form"] button[type="submit"]')
  if (confirmOpenAuth) await confirmDialog(page)
}

export async function closeOverlay(page) {
  await clickControl(page, '.sb-overlay .ant-modal-close')
  await waitOverlaysClosed(page)
}

export async function waitOverlaysClosed(page) {
  await page.waitForFunction(() => !Array.from(document.querySelectorAll('.sb-overlay .ant-modal-wrap')).some((element) => {
    const style = getComputedStyle(element)
    const rect = element.getBoundingClientRect()
    return style.display !== 'none' && style.visibility !== 'hidden' && rect.width > 0 && rect.height > 0
  }))
}

export async function legacyClick(page, text) {
  if (text === 'Batch Import') return openBatchImport(page)
  if (text === 'Import Node' || text === 'Add Node') return page.click('[data-testid="nodes-add-node"]')
  if (text === 'Confirm' && await page.$('[data-testid="batch-import-submit"]')) return page.click('[data-testid="batch-import-submit"]')
  if (text === 'Cancel' && await page.$('[data-testid="batch-import-errors"]')) {
    await clickText(page, 'Close')
    return waitOverlaysClosed(page)
  }
  if (text === 'Save' && await page.$('[data-testid="node-form"] button[type="submit"]')) {
    await saveNodeForm(page)
    await page.waitForFunction(() => {
      const visible = (element) => element && element.getBoundingClientRect().height > 0
      return visible(document.querySelector('.ant-modal-confirm-btns .ant-btn-primary')) ||
        Array.from(document.querySelectorAll('.ant-form-item-explain-error')).some(visible) ||
        !visible(document.querySelector('[data-testid="node-form"]'))
    })
    if (await page.$('.ant-modal-confirm-btns .ant-btn-primary')) await confirmDialog(page)
    return
  }
  return clickText(page, text)
}
