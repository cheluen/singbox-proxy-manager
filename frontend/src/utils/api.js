import axios from 'axios'

const api = axios.create({
  baseURL: '/api',
  timeout: 30000,
})

let authToken = null

api.setToken = (token) => {
  authToken = token
  if (token) {
    api.defaults.headers.common['Authorization'] = token
  } else {
    delete api.defaults.headers.common['Authorization']
  }
}

api.interceptors.response.use(
  (response) => response,
  (error) => {
    const requestPath = String(error.config?.url || '').split('?')[0]
    const isPublicAuthRequest = [
      '/login',
      '/setup/admin-password',
      '/auth/status',
    ].includes(requestPath)

    if (error.response?.status === 401 && authToken && !isPublicAuthRequest) {
      api.setToken(null)
      localStorage.removeItem('token')
      window.dispatchEvent(new CustomEvent('sbpm:unauthorized'))
    }
    return Promise.reject(error)
  }
)

export default api
