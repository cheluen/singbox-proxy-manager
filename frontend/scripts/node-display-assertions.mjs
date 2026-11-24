import assert from 'node:assert/strict'
import { clickControl } from './ui-driver.mjs'

const byTestId = (id) => `[data-testid="${id}"]`

export async function assertInboundUsername(page, id, username) {
  const selector = byTestId(`node-username-${id}`)
  await page.waitForFunction((target, expected) => document.querySelector(target)?.textContent.trim() === expected, {}, selector, username || '—')
}

export async function assertMaskedInboundPassword(page, id, secret, toggleLabel = 'Show inbound password') {
  const selector = `${byTestId(`node-password-${id}`)}[data-masked="true"]`
  await page.waitForSelector(selector, { visible: true })
  const result = await page.$eval(selector, (element, expected, maskSelector) => {
    const row = element.closest('[data-row-id]')
    const mask = element.querySelector(maskSelector)
    const toggle = element.querySelector('button')
    return {
      leaks: row.textContent.includes(expected) || [row, ...row.querySelectorAll('*')].some((child) =>
        Array.from(child.attributes).some((attribute) => attribute.value.includes(expected))),
      width: mask.getBoundingClientRect().width,
      glyphLength: mask.firstElementChild?.textContent?.length ?? 0,
      backdrop: getComputedStyle(mask).backdropFilter,
      glyphFilter: getComputedStyle(mask.firstElementChild).filter,
      maskRole: mask.getAttribute('role'),
      maskLabel: mask.getAttribute('aria-label'),
      hasAriaPressed: toggle.hasAttribute('aria-pressed'),
      toggleLabel: toggle.getAttribute('aria-label'),
    }
  }, secret, byTestId(`node-password-mask-${id}`))
  assert.equal(result.leaks, false, `节点 ${id} 遮蔽时的 DOM 含密码明文`)
  assert.ok(Math.abs(result.width - 72) <= 1, `节点 ${id} 掩码宽度为 ${result.width}px`)
  assert.equal(result.glyphLength, 8, `节点 ${id} 掩码占位字符数不固定：${result.glyphLength}`)
  assert.equal(result.backdrop, 'blur(8px)')
  assert.equal(result.glyphFilter, 'blur(4.5px)')
  assert.equal(result.maskRole, 'img')
  assert.ok(result.maskLabel)
  assert.equal(result.hasAriaPressed, false, '动态切换 aria-label 的眼睛按钮不应同时设置 aria-pressed')
  assert.equal(result.toggleLabel, toggleLabel)
  assert.equal(await page.$(byTestId(`node-password-value-${id}`)), null)
  return result.width
}

export async function assertEmptyInboundPassword(page, id) {
  const selector = byTestId(`node-password-${id}`)
  await page.waitForFunction((target) => document.querySelector(target)?.textContent.trim() === '—', {}, selector)
  assert.equal(await page.$(`${selector} button, ${byTestId(`node-password-mask-${id}`)}`), null)
}

async function assertUnclippedText(page, selector, expected, label) {
  await page.waitForFunction((target, text) => document.querySelector(target)?.textContent === text, {}, selector, expected)
  await page.waitForSelector(selector, { visible: true })
  assert.ok(await page.$eval(selector, (element) => {
    const bounds = element.getBoundingClientRect()
    const range = document.createRange()
    range.selectNodeContents(element)
    return Array.from(range.getClientRects()).every((line) => line.width <= bounds.width + 1 && line.bottom <= bounds.bottom + 1) &&
      element.scrollWidth <= element.clientWidth + 1 && element.scrollHeight <= element.clientHeight + 1
  }), `${label}被截断`)
}

export async function assertRevealedInboundPassword(page, id, secret, toggleLabel = 'Hide inbound password') {
  const selector = byTestId(`node-password-${id}`)
  const valueSelector = `${selector}[data-masked="false"] ${byTestId(`node-password-value-${id}`)}`
  await assertUnclippedText(page, valueSelector, secret, `节点 ${id} 的密码明文`)
  assert.equal(await page.$eval(valueSelector, (element) => getComputedStyle(element).whiteSpace), 'pre-wrap', '展开密码必须保留首尾及连续空格')
  assert.equal(await page.$(byTestId(`node-password-mask-${id}`)), null)
  const toggle = byTestId(`node-password-toggle-${id}`)
  assert.deepEqual(await page.$eval(toggle, (button) => [button.hasAttribute('aria-pressed'), button.getAttribute('aria-label')]), [false, toggleLabel])
}

export async function assertFullNodeName(page, id, name, title = 'Full node name') {
  const selector = byTestId(`node-full-name-${id}`)
  await assertUnclippedText(page, selector, name, `节点 ${id} 的完整名称`)
  const position = await page.$eval(selector, (element) => {
    const section = element.closest('.sb-full-name-section')
    const group = section?.parentElement
    const nameBounds = section?.getBoundingClientRect()
    const remarkBounds = group?.querySelector('.sb-remark')?.getBoundingClientRect()
    return {
      title: section?.querySelector('h3')?.textContent,
      aboveRemark: group?.classList.contains('sb-detail-note-group') && nameBounds?.bottom < remarkBounds?.top &&
        Math.abs(nameBounds.left - remarkBounds.left) <= 1,
    }
  })
  assert.deepEqual(position, { title, aboveRemark: true })
}

export async function assertRouteId(page, id, value, label = 'Route ID') {
  const section = byTestId(`node-route-id-section-${id}`)
  await page.waitForSelector(section, { visible: Boolean(value), hidden: !value })
  if (!value) return
  assert.equal(await page.$eval(byTestId(`node-route-id-label-${id}`), (element) => element.textContent), label)
  assert.equal(await page.$eval(byTestId(`node-route-id-value-${id}`), (element) => element.textContent), value)
}

export async function assertCopiedText(page, selector, value) {
  await clickControl(page, selector)
  await page.waitForFunction(async (expected) => (await navigator.clipboard.readText()) === expected, { polling: 100 }, value)
}

export async function assertUnclippedTableHeaders(page) {
  const clipped = await page.$$eval('.dusk-head span', (spans) => spans
    .filter((span) => span.textContent.trim().length > 0 && span.offsetParent !== null && getComputedStyle(span).display !== 'none')
    .filter((span) => span.scrollWidth > span.clientWidth + 1)
    .map((span) => ({ text: span.textContent.trim(), scrollWidth: span.scrollWidth, clientWidth: span.clientWidth })))
  assert.deepEqual(clipped, [], `表头文字被裁切：${JSON.stringify(clipped)}`)
}

const EXPECTED_META_COLUMNS = [
  'dusk-col--port',
  'dusk-col--user',
  'dusk-col--pass',
  'dusk-col--type',
  'dusk-col--server',
  'dusk-col--region',
  'dusk-col--ip',
  'dusk-col--latency',
]

export async function assertColumnOrder(page) {
  const result = await page.evaluate((expectedCols) => {
    const extractCols = (container) => Array.from(container.children).map((el) =>
      expectedCols.find((cls) => el.classList.contains(cls)) || el.className)
    const visibleBounds = (container) => Array.from(container.children)
      .filter((el) => el.offsetParent !== null && getComputedStyle(el).display !== 'none')
      .map((el) => {
        const rect = el.getBoundingClientRect()
        const contentRight = Math.max(rect.right, ...Array.from(el.children, (child) => child.getBoundingClientRect().right))
        return { cls: expectedCols.find((c) => el.classList.contains(c)), left: rect.left, right: contentRight }
      })
    const headMeta = document.querySelector('.dusk-head .dusk-row__meta')
    const firstRowMeta = document.querySelector('.dusk-row .dusk-row__meta')
    const headBoxes = headMeta ? visibleBounds(headMeta) : []
    const rowBoxes = firstRowMeta ? visibleBounds(firstRowMeta) : []
    const noOverlap = (boxes) => boxes.every((item, idx) => idx === 0 || (item.left > boxes[idx - 1].left && boxes[idx - 1].right <= item.left + 1))
    return {
      headCols: headMeta ? extractCols(headMeta) : [],
      rowCols: firstRowMeta ? extractCols(firstRowMeta) : [],
      headNonOverlapping: noOverlap(headBoxes),
      rowNonOverlapping: noOverlap(rowBoxes),
      rowBoxes,
    }
  }, EXPECTED_META_COLUMNS)
  assert.deepEqual(result.headCols, EXPECTED_META_COLUMNS, '表头入站/出站列顺序与约定不符')
  assert.deepEqual(result.rowCols, EXPECTED_META_COLUMNS, '节点行入站/出站列顺序与约定不符')
  assert.equal(result.headNonOverlapping, true, '可见表头水平位置未递增或发生重叠')
  assert.equal(result.rowNonOverlapping, true, `节点行单元格内容越界重叠：${JSON.stringify(result.rowBoxes)}`)
}
