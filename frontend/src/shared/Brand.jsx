import { PROJECT_BRAND, PROJECT_DESCRIPTOR, PROJECT_MARK_FILE } from '../core/project'

export function BrandLogo({ size = 32 }) {
  return (
    <span className="sb-logo" style={{ width: size, height: size }} aria-hidden="true">
      <img src={`${import.meta.env.BASE_URL}${PROJECT_MARK_FILE}`} alt="" width={size} height={size} />
    </span>
  )
}

export function BrandWordmark() {
  return (
    <span className="sb-wordmark">
      <span className="sb-wordmark__name">{PROJECT_BRAND}</span>{' '}
      <span className="sb-wordmark__descriptor">{PROJECT_DESCRIPTOR}</span>
    </span>
  )
}
