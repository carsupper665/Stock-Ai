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
  <div class="wrap">
    <section class="card col">
      <div class="row"><i class="dot square" data-tone="acc"></i><b>Agent 控制台</b></div>
      <template v-if="session.token">
        <p>已以 <b>{{ session.name }}</b> 身分登入。</p>
        <div class="row"><RouterLink to="/">進入控制台</RouterLink><button @click="logout">登出</button></div>
      </template>
      <form v-else class="col" @submit.prevent="submit">
        <label>
          USER TOKEN
          <input v-model="token" class="mono" type="password" autocomplete="off" required />
        </label>
        <p class="note">貼交易後端 <code>.env</code> 的 <code>USER_TOKEN</code>。未登入不能有任何互動；Agent／LLM Server 的管理憑證由代理注入，這裡不用貼。</p>
        <button data-tone="acc" :disabled="busy">登入</button>
        <p v-if="error" data-tone="neg">{{ error }}</p>
      </form>
    </section>
  </div>
</template>

<style scoped>
.wrap {
  min-height: 100vh;
  display: grid;
  place-items: center;
  padding: var(--pad);
}
.card {
  width: 100%;
  max-width: 380px;
}
</style>
