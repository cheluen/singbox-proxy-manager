import DuskLogin from './Login'
import DuskPanel from './Panel'
import './dusk.css'

export default function DuskDesign({ session }) {
  return session.token ? <DuskPanel session={session} /> : <DuskLogin session={session} />
}
