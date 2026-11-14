import { randomInt } from 'node:crypto'
import net from 'node:net'

// 使用独立的非临时端口段，避免测试与验收实例或客户端源端口争用。
export async function findTestPortRange(size = 64) {
  if (!Number.isSafeInteger(size) || size < 1 || size > 512) throw new RangeError('测试端口段大小无效')
  for (let attempt = 0; attempt < 32; attempt += 1) {
    const startPort = randomInt(20000, 29000 - size)
    const probes = []
    try {
      for (let offset = 0; offset < size; offset += 1) {
        const probe = net.createServer()
        probes.push(probe)
        await new Promise((resolve, reject) => {
          probe.once('error', reject)
          probe.listen(startPort + offset, '127.0.0.1', resolve)
        })
      }
      return startPort
    } catch (error) {
      if (error.code !== 'EADDRINUSE') throw error
    } finally {
      await Promise.all(probes.map((probe) => new Promise((resolve) => probe.close(resolve))))
    }
  }
  throw new Error('找不到独立的测试入站端口段')
}
