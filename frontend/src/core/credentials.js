// 生成入站认证用的随机凭据。字符集去掉了易混淆的 0/O、1/l/I。
const ALPHABET = 'abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789'

export const randomToken = (length) => {
  // 拒绝采样：丢弃落在不完整区间内的随机字节，保证每个字符等概率出现。
  const limit = 256 - (256 % ALPHABET.length)
  let output = ''
  while (output.length < length) {
    const bytes = new Uint8Array(length * 2)
    globalThis.crypto.getRandomValues(bytes)
    for (const byte of bytes) {
      if (byte < limit && output.length < length) output += ALPHABET[byte % ALPHABET.length]
    }
  }
  return output
}

export const randomCredentials = () => ({ username: `u${randomToken(7)}`, secret: randomToken(16) })
