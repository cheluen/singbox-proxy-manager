import ReactDOM from 'react-dom/client'
import '@fontsource-variable/inter'
import '@fontsource-variable/jetbrains-mono'
import './core/i18n'
import App from './App'
import './theme/base.css'
import './shared/shared.css'
import './shared/forms.css'

ReactDOM.createRoot(document.getElementById('root')).render(<App />)
