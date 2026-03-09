<template>
  <section class="login-card panel">
    <div>
      <p class="eyebrow">Admin Session Login</p>
      <h1>Control Room Access</h1>
      <p class="copy">Use the root or admin session account to configure sandboxes, datasets, and monitoring.</p>
    </div>
    <form class="grid-gap" @submit.prevent="submit">
      <label>
        Username
        <input v-model="form.username" class="control" autocomplete="username" placeholder="Enter admin username" />
      </label>
      <label>
        Password
        <input v-model="form.password" class="control" type="password" autocomplete="current-password" placeholder="Enter password" />
      </label>
      <p v-if="auth.errorMessage" class="error-copy">{{ auth.errorMessage }}</p>
      <button class="btn primary" :disabled="auth.pending">{{ auth.pending ? 'Signing in…' : 'Sign in' }}</button>
    </form>
  </section>
</template>

<script setup lang="ts">
import { reactive } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

const router = useRouter()
const auth = useAuthStore()
const form = reactive({ username: '', password: '' })

async function submit() {
  try {
    await auth.login(form)
    await router.push('/admin/sandboxes')
  } catch {
    // The auth store already exposes the backend error to the form UI.
  }
}
</script>

<style scoped>
.login-card {
  width: min(460px, 100%);
  padding: 1.3rem;
}
.eyebrow {
  margin: 0 0 0.35rem;
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: var(--muted);
  font-size: 0.75rem;
}
.copy,
.error-copy {
  color: var(--muted);
}
.error-copy {
  color: #ffb4b4;
}
</style>
