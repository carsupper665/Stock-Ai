import { createApp } from 'vue'

import './theme.css'
import App from './App.vue'
import { router } from './core/router.js'

createApp(App).use(router).mount('#app')
