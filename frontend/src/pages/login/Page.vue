<script setup>
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { login } from '../../core/auth.js'
import { logout, session } from '../../core/session.js'

const route = useRoute()
const router = useRouter()
const token = ref(import.meta.env.VITE_DEV_TOKEN ?? '')
const error = ref('')
const busy = ref(false)

async function submit() {
  busy.value = true
  error.value = ''
  try {
    await login({ token: token.value })
    router.push(route.query.redirect ?? '/')
  } catch (e) {
    error.value = e.message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <main>
    <h1>登入</h1>
    <template v-if="session.token">
      <p>已以 {{ session.name }} 身分登入。</p>
      <button @click="logout">登出</button>
    </template>
    <form v-else @submit.prevent="submit">
      <label>
        USER Token
        <input v-model="token" type="password" autocomplete="off" required />
      </label>
      <button :disabled="busy">登入</button>
      <p v-if="error" data-tone="danger">{{ error }}</p>
    </form>
  </main>
</template>

<style scoped>
form {
  display: flex;
  flex-direction: column;
  gap: var(--space);
  max-width: 24em;
}
</style>
