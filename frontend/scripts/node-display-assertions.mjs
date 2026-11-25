import assert from 'node:assert/strict'
import { clickControl } from './ui-driver.mjs'

const byTestId = (id) => `[data-testid="${id}"]`

export async function assertInboundUsername(page, id, username) {
  const selector = byTestId(`node-username-${id}`)
  await page.waitForFunction((target, expected) => document.querySelector(target)?.textContent.trim() === expected, {}, selector, username || '—')
}

export async function assertMaskedInboundPassword(page, id, secret, toggleLabel = 'Click to show inbound password') {
  const selector = `${byTestId(`node-password-${id}`)}[data-masked="true"]`
  await page.waitForSelector(selector, { visible: true })
  await page.evaluate(async (target, maskSelector) => {
    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))
    const glyphs = document.querySelector(target)?.querySelector(`${maskSelector} .sb-pass-mask__glyphs`)
    const animations = glyphs?.getAnimations?.() || []
    await Promise.allSettled(animations.map((anim) => anim.finished))
  }, selector, byTestId(`node-password-mask-${id}`))
  const result = await page.$eval(selector, (element, expected, maskSelector) => {
    const row = element.closest('[data-row-id]')
    const mask = element.querySelector(maskSelector)
    const veil = mask?.querySelector('.sb-pass-mask__veil')
    const glyphs = mask?.querySelector('.sb-pass-mask__glyphs')
    return {
      leaks: row.textContent.includes(expected) || [row, ...row.querySelectorAll('*')].some((child) =>
        Array.from(child.attributes).some((attribute) => attribute.value.includes(expected))),
      buttonCount: element.querySelectorAll('button').length,
      maskIsButton: mask?.tagName === 'BUTTON',
      width: mask?.getBoundingClientRect().width ?? 0,
      borderWidth: mask ? getComputedStyle(mask).borderTopWidth : '',
      glyphLength: glyphs?.textContent?.length ?? 0,
      backdrop: veil ? getComputedStyle(veil).backdropFilter : '',
      maskImage: veil ? getComputedStyle(veil).maskImage : '',
      glyphFilter: glyphs ? getComputedStyle(glyphs).filter : '',
      hasAriaPressed: mask?.hasAttribute('aria-pressed') ?? false,
      toggleLabel: mask?.getAttribute('aria-label'),
    }
  }, secret, byTestId(`node-password-mask-${id}`))
  assert.equal(result.leaks, false, `节点 ${id} 遮蔽时的 DOM 含密码明文`)
  assert.equal(result.buttonCount, 1, `节点 ${id} 密码列只应有掩码触发区，不应再渲染额外眼睛按钮`)
  assert.equal(result.maskIsButton, true, `节点 ${id} 掩码区域应为原生按钮`)
  assert.ok(Math.abs(result.width - 72) <= 1, `节点 ${id} 掩码宽度为 ${result.width}px`)
  assert.equal(result.borderWidth, '0px', `节点 ${id} 掩码不应有生硬边框`)
  assert.equal(result.glyphLength, 8, `节点 ${id} 掩码占位字符数不固定：${result.glyphLength}`)
  assert.equal(result.backdrop, 'blur(6px)')
  assert.ok(result.maskImage.includes('radial-gradient') && result.maskImage.includes('closest-side'), `节点 ${id} 掩码未启用 closest-side 柔边羽化：${result.maskImage}`)
  assert.match(result.glyphFilter, /^blur\((3\.8|3\.1)px\)$/, `节点 ${id} 掩码模糊滤镜异常：${result.glyphFilter}`)
  assert.equal(result.hasAriaPressed, false, '动态切换 aria-label 的控件不应同时设置 aria-pressed')
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

export async function assertRevealedInboundPassword(page, id, secret, toggleLabel = 'Click to hide inbound password') {
  const selector = byTestId(`node-password-${id}`)
  const valueSelector = `${selector}[data-masked="false"] ${byTestId(`node-password-value-${id}`)}`
  await assertUnclippedText(page, valueSelector, secret, `节点 ${id} 的密码明文`)
  assert.equal(await page.$eval(valueSelector, (element) => getComputedStyle(element).whiteSpace), 'pre-wrap', '展开密码必须保留首尾及连续空格')
  assert.equal(await page.$(byTestId(`node-password-mask-${id}`)), null)
  assert.equal(await page.$(`${selector} button`), null, '展开密码后不应渲染额外眼睛按钮')
  assert.deepEqual(
    await page.$eval(valueSelector, (el) => [
      el.getAttribute('role'),
      el.hasAttribute('aria-pressed'),
      el.hasAttribute('aria-label'),
      el.getAttribute('aria-description'),
      el.getAttribute('data-tip'),
    ]),
    ['button', false, false, toggleLabel, toggleLabel],
  )
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

export async function assertDetailLayoutOrderAndBalance(page, id, viewportWidth) {
  const layout = await page.$eval(`[data-testid="node-inspector"][data-node-id="${id}"]`, (inspector) => {
    const cards = ['inbound', 'outbound', 'meta'].map((group) => {
      const el = inspector.querySelector(`[data-detail-group="${group}"]`)
      const rect = el?.getBoundingClientRect()
      return {
        group,
        exists: Boolean(el),
        left: rect?.left ?? 0,
        right: rect?.right ?? 0,
        top: rect?.top ?? 0,
        bottom: rect?.bottom ?? 0,
        width: rect?.width ?? 0,
        height: rect?.height ?? 0,
      }
    })
    return {
      inbound: cards[0],
      outbound: cards[1],
      meta: cards[2],
    }
  })
  assert.equal(layout.inbound.exists && layout.outbound.exists && layout.meta.exists, true, '详情三大逻辑分组卡片缺失')
  if (viewportWidth > 1200) {
    assert.ok(layout.inbound.right <= layout.outbound.left + 1, '宽屏展开详情中入站卡片必须在出站卡片左侧')
    assert.ok(layout.outbound.right <= layout.meta.left + 1, '宽屏展开详情中出站卡片必须在备注卡片左侧')
    assert.ok(Math.abs(layout.inbound.top - layout.outbound.top) <= 2 && Math.abs(layout.outbound.top - layout.meta.top) <= 2, '宽屏三卡片顶边未对齐')
    assert.ok(Math.abs(layout.inbound.bottom - layout.outbound.bottom) <= 2 && Math.abs(layout.outbound.bottom - layout.meta.bottom) <= 2, '宽屏三卡片底边未等高对齐，存在不对称留白')
  } else if (viewportWidth >= 900) {
    assert.ok(layout.inbound.right <= layout.outbound.left + 1, '中屏展开详情中入站卡片必须在出站卡片左侧')
    assert.ok(Math.abs(layout.inbound.top - layout.outbound.top) <= 2 && Math.abs(layout.inbound.bottom - layout.outbound.bottom) <= 2, '中屏入站与出站卡片未等高对齐')
    assert.ok(layout.meta.top >= layout.inbound.bottom - 1 && Math.abs(layout.meta.left - layout.inbound.left) <= 2 && Math.abs(layout.meta.right - layout.outbound.right) <= 2, '中屏名称与备注卡片应横跨底部整行保持对称')
  } else {
    assert.ok(layout.inbound.bottom <= layout.outbound.top + 1, '窄屏展开详情中入站卡片必须在出站卡片上方')
    assert.ok(layout.outbound.bottom <= layout.meta.top + 1, '窄屏展开详情中出站卡片必须在备注卡片上方')
  }
}
