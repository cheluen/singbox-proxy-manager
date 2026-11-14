import { frontendBuildVersion } from '../core/version'
import { RepoLink } from '../shared/chrome'
import { BrandLogo, BrandWordmark } from '../shared/Brand'
import { LoginForm } from '../shared/LoginForm'
import { LangSwitch } from '../shared/primitives'

export default function DuskLogin({ session }) {
  return (
    <div className="dusk-login">
      <div className="dusk-login__lang"><LangSwitch /></div>
      <main className="dusk-login__card">
        <header className="dusk-login__head">
          <BrandLogo size={48} />
          <h1 lang="en" data-testid="project-brand"><BrandWordmark /></h1>
        </header>
        <LoginForm session={session} />
      </main>
      <footer className="dusk-login__foot">
        <RepoLink withLabel />
        <span className="mono sb-dim">v{frontendBuildVersion}</span>
      </footer>
    </div>
  )
}
