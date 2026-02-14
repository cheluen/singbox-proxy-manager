import React, { useEffect, useState } from 'react'
import { ConfigProvider, theme, message } from 'antd'
import Login from './components/Login'
import Dashboard from './components/Dashboard'
import api from './utils/api'
import { ensureLatestFrontendBuild } from './utils/version'

function App() {
  const [token, setToken] = useState(() => {
    const savedToken = localStorage.getItem('token')
    if (savedToken) {
      api.setToken(savedToken)
    }
    return savedToken
  })

  useEffect(() => {
    let cancelled = false

    const checkVersionSkew = async () => {
      try {
        const response = await api.get('/version')
        if (cancelled) {
          return
        }
        ensureLatestFrontendBuild(response.data?.version)
      } catch {
        // Ignore transient network failures; normal page flow continues.
      }
    }

    checkVersionSkew()
    return () => {
      cancelled = true
    }
  }, [])

  const handleLogin = (newToken) => {
    localStorage.setItem('token', newToken)
    setToken(newToken)
    api.setToken(newToken)
    message.success('Login successful!')
  }

  const handleLogout = () => {
    localStorage.removeItem('token')
    setToken(null)
    api.setToken(null)
    message.info('Logged out')
  }

  return (
    <ConfigProvider
      theme={{
        algorithm: theme.defaultAlgorithm,
        token: {
          colorPrimary: '#667eea',
        },
      }}
    >
      {!token ? (
        <Login onLogin={handleLogin} />
      ) : (
        <Dashboard onLogout={handleLogout} />
      )}
    </ConfigProvider>
  )
}

export default App
