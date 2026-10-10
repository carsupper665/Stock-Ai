<script setup>
import { ref } from 'vue'
import { useRoute } from 'vue-router'

import { login } from '../../core/auth.js'
import * as oidc from '../../core/providers/oidc.js'
import { isAuthenticated, session } from '../../core/session.js'

const route = useRoute()
const error = ref('')
const busy = ref(false)

async function submit() {
  busy.value = true
  error.value = ''
  try {
    await login({ returnTo: route.query.redirect ?? '/' })
  } catch (e) {
    error.value = e.message
  } finally {
    busy.value = false
  }
}

function logout() { oidc.logout() }

if (route.query.auth_error) error.value = '登入未完成或已失效，請重試。'
else if (route.query.expired) error.value = '登入已過期，請重新登入。'
</script>

<template>
  <div class="wrap">
    <section class="card col">
      <div class="row"><i class="dot square" data-tone="acc"></i><b>Agent 控制台</b></div>
      <template v-if="isAuthenticated()">
        <p>已以 <b>{{ session.name }}</b> 身分登入。</p>
        <div class="row"><RouterLink to="/">進入控制台</RouterLink><button @click="logout">登出</button></div>
      </template>
      <form v-else class="col" @submit.prevent="submit">
        <p class="note">登入由此環境專用的身分提供者完成；瀏覽器不會取得或儲存 access token。</p>
        <button data-tone="acc" :disabled="busy">使用企業帳號登入</button>
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
