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
  legend: { textStyle: { color: '#98a7c2' } },
  grid: { left: 36, right: 18, top: 36, bottom: 24 },
  xAxis: {
    type: 'category',
    boundaryGap: false,
    axisLabel: { color: '#98a7c2' },
    data: props.series[0]?.points.map((point) => new Date(point.timestamp).toLocaleTimeString()) ?? [],
  },
  yAxis: {
    type: 'value',
    axisLabel: { color: '#98a7c2' },
    splitLine: { lineStyle: { color: 'rgba(149, 164, 187, 0.12)' } },
  },
  series: props.series.map((serie, index) => ({
    name: serie.name,
    type: 'line',
    smooth: true,
    showSymbol: false,
    lineStyle: { width: 2 },
    itemStyle: { color: index % 2 === 0 ? '#4ea1ff' : '#34d399' },
    areaStyle: { opacity: 0.08 },
    data: serie.points.map((point) => point.value),
  })),
}))
</script>

<style scoped>
.chart-wrap {
  min-height: 240px;
  padding: 0.75rem;
}
.chart {
  min-height: 220px;
}
</style>
