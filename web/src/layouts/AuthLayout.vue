<template>
  <main class="auth-shell">
    <section class="auth-panel panel">
      <RouterView />
    </section>
  </main>
</template>

<style scoped>
.auth-shell {
  min-height: 100vh;
  display: grid;
  place-items: center;
  padding: 1.5rem;
}

.auth-panel {
  width: min(520px, 100%);
  padding: 1.25rem;
  background: linear-gradient(180deg, #ffffff 0%, #f8fbff 100%);
}
</style>

