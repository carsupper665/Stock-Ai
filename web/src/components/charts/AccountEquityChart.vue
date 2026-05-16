<template>
  <div class="chart-wrap panel">
    <VChart class="chart" :option="option" autoresize />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import { LineChart } from 'echarts/charts'
import VChart from 'vue-echarts'
import type { ChartPoint } from '@/types/domain'

use([CanvasRenderer, GridComponent, LegendComponent, TooltipComponent, LineChart])

const props = defineProps<{
  title?: string
  series: Array<{ name: string; points: ChartPoint[] }>
}>()

const option = computed(() => ({
  backgroundColor: 'transparent',
  tooltip: { trigger: 'axis' },
  legend: { textStyle: { color: '#64748b', fontWeight: 600 } },
  grid: { left: 36, right: 20, top: 36, bottom: 24 },
  xAxis: {
    type: 'category',
    boundaryGap: false,
    axisLabel: { color: '#64748b' },
    data: props.series[0]?.points.map((point) => new Date(point.timestamp).toLocaleTimeString()) ?? [],
  },
  yAxis: {
    type: 'value',
    axisLabel: { color: '#64748b' },
    splitLine: { lineStyle: { color: 'rgba(148, 163, 184, 0.24)' } },
  },
  series: props.series.map((serie, index) => ({
    name: serie.name,
    type: 'line',
    smooth: true,
    showSymbol: false,
    lineStyle: { width: 2.4 },
    itemStyle: { color: index % 2 === 0 ? '#2563eb' : '#16a34a' },
    areaStyle: { opacity: 0.1 },
    data: serie.points.map((point) => point.value),
  })),
}))
</script>

<style scoped>
.chart-wrap {
  min-height: 250px;
  padding: 0.9rem;
  background: linear-gradient(180deg, #ffffff 0%, #f8fbff 100%);
}

.chart {
  min-height: 228px;
}
</style>

