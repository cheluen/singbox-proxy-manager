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
    if (error.response?.status === 401) {
      localStorage.removeItem('token')
      window.location.reload()
    }
    return Promise.reject(error)
  }
)

export default api
