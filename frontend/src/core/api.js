import axios from 'axios'

const api = axios.create({
  baseURL: '/api',
  timeout: 30000,
})

// 外部管理操作完成后通知面板重新读取真实状态。
export const EXTERNAL_CHANGE_EVENT = 'sbpm-dusk:external-change'

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
    const isPublicAuthRequest = requestPath === '/login'

    const requestToken = error.config?.headers?.Authorization
    if (error.response?.status === 401 && authToken && requestToken === authToken && !isPublicAuthRequest) {
      api.setToken(null)
      window.dispatchEvent(new CustomEvent('sbpm-dusk:unauthorized'))
    }
    return Promise.reject(error)
  }
)

export default api
