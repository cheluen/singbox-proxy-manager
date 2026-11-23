// 构建后预压缩静态资源。HTML 入口由 Go 写入运行配置后压缩。
import { readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { brotliCompressSync, constants, gzipSync } from 'node:zlib'

const DIST = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', 'dist')
const COMPRESSIBLE = new Set(['.js', '.css', '.svg', '.json', '.txt', '.map'])
const MIN_BYTES = 1024

const walk = (dir) =>
  readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const full = path.join(dir, entry.name)
    return entry.isDirectory() ? walk(full) : [full]
  })

let count = 0
let rawTotal = 0
let brTotal = 0
for (const file of walk(DIST)) {
  if (!COMPRESSIBLE.has(path.extname(file)) || statSync(file).size < MIN_BYTES) continue
  const source = readFileSync(file)
  const brotli = brotliCompressSync(source, {
    params: { [constants.BROTLI_PARAM_QUALITY]: 11, [constants.BROTLI_PARAM_SIZE_HINT]: source.length },
  })
  writeFileSync(`${file}.gz`, gzipSync(source, { level: 9 }))
  writeFileSync(`${file}.br`, brotli)
  count += 1
  rawTotal += source.length
  brTotal += brotli.length
}
const kb = (bytes) => `${(bytes / 1024).toFixed(1)} kB`
process.stdout.write(`precompressed ${count} files: ${kb(rawTotal)} -> ${kb(brTotal)} (brotli)\n`)
