import React, { useState, useEffect } from 'react'
import { ConfigProvider, theme, message } from 'antd'
import Login from './components/Login'
import Dashboard from './components/Dashboard'
import api from './utils/api'

function App() {
  const [token, setToken] = useState(localStorage.getItem('token'))
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (token) {
      api.setToken(token)
    }
  }, [token])

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
