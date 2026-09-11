<script setup>
import { nav } from './core/pages.js'
import { logout, session } from './core/session.js'
import { setTheme } from './core/theme.js'

const theme = localStorage.getItem('theme') ?? ''
</script>

<template>
  <header>
    <nav v-if="session.token">
      <RouterLink v-for="r in nav" :key="r.name" :to="{ name: r.name }">{{ r.meta.title }}</RouterLink>
    </nav>
    <select :value="theme" @change="setTheme($event.target.value)">
      <option value="">跟系統</option>
      <option value="light">亮</option>
      <option value="dark">暗</option>
    </select>
    <template v-if="session.token">
      <span>{{ session.name }}</span>
      <button @click="logout">登出</button>
    </template>
  </header>
  <RouterView />
</template>

<style scoped>
header {
  display: flex;
  align-items: center;
  gap: var(--space);
  padding: var(--space) calc(var(--space) * 2);
  border-bottom: 1px solid var(--line);
}
nav {
  display: flex;
  gap: calc(var(--space) * 2);
  flex: 1;
}
</style>
