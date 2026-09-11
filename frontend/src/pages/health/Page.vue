<script setup>
import { useLoad } from '../../core/load.js'
import { getHealth } from './api.js'

const { data, error, loading, at, reload } = useLoad(getHealth)
</script>

<template>
  <main>
    <h1>交易後端</h1>
    <p>
      <button :disabled="loading" @click="reload">重新整理</button>
      <span v-if="at" data-tone="muted">最後更新 {{ at.toLocaleTimeString() }}</span>
    </p>
    <p v-if="error" data-tone="danger">{{ error.message }}</p>
    <table v-else-if="data">
      <tbody>
        <tr>
          <th>status</th>
          <td :data-tone="data.status === 'ok' ? 'ok' : 'danger'">{{ data.status }}</td>
        </tr>
        <tr>
          <th>database</th>
          <td>{{ data.database }}</td>
        </tr>
      </tbody>
    </table>
  </main>
</template>
