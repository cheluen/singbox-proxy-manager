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

  const handleLogout = async () => {
    try {
      await api.post('/logout')
    } catch {
      // Even if backend logout fails, clear local session to force re-auth.
    }
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
          colorPrimary: '#7d8da6',
          colorSuccess: '#7da08f',
          colorWarning: '#c4a77d',
          colorError: '#be8a7e',
          colorInfo: '#7d8da6',
          colorBgLayout: '#f4f1ec',
          colorBgContainer: '#fbfaf7',
          colorBorder: '#d7d2c9',
          borderRadius: 12,
          fontFamily: '"Noto Sans SC", "Source Han Sans SC", "PingFang SC", "Microsoft YaHei", sans-serif',
        },
        components: {
          Layout: {
            headerBg: '#6f8198',
            bodyBg: '#f4f1ec',
          },
          Button: {
            borderRadius: 10,
          },
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
