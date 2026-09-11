import { reactive } from 'vue'

export const session = reactive({
  token: sessionStorage.getItem('token'),
  name: sessionStorage.getItem('name'),
})

export function save({ token, name }) {
  Object.assign(session, { token, name })
  sessionStorage.setItem('token', token)
  sessionStorage.setItem('name', name)
}

export function logout() {
  sessionStorage.clear()
  location.assign('/login')
}
