import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { createAppRouter } from './router'
import { setUnauthorizedHandler } from './services/api/client'
import { useAuthStore } from './stores/auth'
import './styles/base.css'

const app = createApp(App)
const pinia = createPinia()
const router = createAppRouter(pinia)

setUnauthorizedHandler(() => {
  const auth = useAuthStore(pinia)
  auth.handleUnauthorized()
  if (router.currentRoute.value.path !== '/login') {
    void router.push('/login')
  }
})

app.use(pinia)
app.use(router)
app.mount('#app')
