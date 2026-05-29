import { useCallback, useEffect, useState, type ReactNode } from 'react';
import ReactECharts from 'echarts-for-react';
import { CandlestickChart, RefreshCw, Loader, AlertTriangle, ChevronDown } from 'lucide-react';
import type { Theme, Kline, IndicatorsResponse, IndicatorPoint } from '../types';
import { getLiveKlines, getLiveIndicators } from '../api/adminMarket';

interface TechnicalAnalysisViewProps {
  theme: Theme;
}

const SYMBOLS = [
  'BTCUSDT', 'ETHUSDT', 'BNBUSDT', 'SOLUSDT', 'ADAUSDT',
  'XRPUSDT', 'DOGEUSDT', 'LTCUSDT', 'AVAXUSDT', 'DOTUSDT',
];

const INTERVALS = ['1m', '5m', '15m', '1h', '4h', '1d'];

// Echarts candlestick format: [open, close, low, high]
function toEChartsCandlestick(klines: Kline[]): [number, number, number, number][] {
  return klines.map((k) => [k.open, k.close, k.low, k.high]);
}

function seriesFromIndicator(name: string, pts: IndicatorPoint[], color: string) {
  return {
    name,
    type: 'line',
    data: pts.map((p) => [p.time, p.value]),
    smooth: true,
    symbol: 'none',
    lineStyle: { width: 1.5, color },
    itemStyle: { color },
  };
}

export default function TechnicalAnalysisView({ theme }: TechnicalAnalysisViewProps) {
  const isDark = theme === 'dark';

  const [symbol, setSymbol] = useState('BTCUSDT');
  const [interval, setInterval] = useState('1h');
  const [klines, setKlines] = useState<Kline[]>([]);
  const [indicators, setIndicators] = useState<IndicatorsResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const fetchData = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const [k, ind] = await Promise.all([
        getLiveKlines(symbol, interval),
        getLiveIndicators(symbol, interval),
      ]);
      setKlines(k);
      setIndicators(ind);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : 'Failed to load market data.');
    } finally {
      setLoading(false);
    }
  }, [symbol, interval]);

  useEffect(() => { void fetchData(); }, [fetchData]);

  // ── ECharts options ───────────────────────────────────────────────────────

  const bgColor = isDark ? '#111827' : '#ffffff';
  const textColor = isDark ? '#9ca3af' : '#6b7280';
  const gridColor = isDark ? '#1f2937' : '#f3f4f6';
  const axisLineColor = isDark ? '#374151' : '#e5e7eb';

  const times = klines.map((k) => k.at);

  // Volume bar colors
  const volumeData = klines.map((k) => ({
    value: k.volume,
    itemStyle: { color: k.close >= k.open ? '#22c55e' : '#ef4444', opacity: 0.5 },
  }));

  const ind = indicators?.indicators ?? {};

  const candlestickOption = {
    backgroundColor: bgColor,
    animation: false,
    tooltip: {
      trigger: 'axis' as const,
      axisPointer: { type: 'cross' as const },
      backgroundColor: isDark ? '#1f2937' : '#fff',
      textStyle: { color: textColor, fontSize: 11 },
    },
    legend: {
      top: 4,
      right: 60,
      textStyle: { color: textColor, fontSize: 10 },
      itemWidth: 16,
      itemHeight: 8,
    },
    dataZoom: [
      { type: 'inside', xAxisIndex: [0, 1, 2], start: 60, end: 100 },
      { type: 'slider', xAxisIndex: [0, 1, 2], start: 60, end: 100, height: 20, bottom: 4,
        textStyle: { color: textColor }, borderColor: axisLineColor, fillerColor: isDark ? 'rgba(99,102,241,0.15)' : 'rgba(99,102,241,0.1)' },
    ],
    grid: [
      { left: 60, right: 12, top: 40, height: '50%' },
      { left: 60, right: 12, top: '62%', height: '12%' },
      { left: 60, right: 12, top: '78%', height: '10%' },
    ],
    xAxis: [
      { type: 'category', data: times, gridIndex: 0, axisLabel: { color: textColor, fontSize: 10 }, axisLine: { lineStyle: { color: axisLineColor } }, splitLine: { lineStyle: { color: gridColor } } },
      { type: 'category', data: times, gridIndex: 1, axisLabel: { show: false }, axisLine: { lineStyle: { color: axisLineColor } } },
      { type: 'category', data: times, gridIndex: 2, axisLabel: { color: textColor, fontSize: 9 }, axisLine: { lineStyle: { color: axisLineColor } } },
    ],
    yAxis: [
      { scale: true, gridIndex: 0, axisLabel: { color: textColor, fontSize: 10 }, axisLine: { lineStyle: { color: axisLineColor } }, splitLine: { lineStyle: { color: gridColor } } },
      { scale: true, gridIndex: 1, axisLabel: { color: textColor, fontSize: 9 }, axisLine: { lineStyle: { color: axisLineColor } }, splitLine: { show: false } },
      { scale: true, gridIndex: 2, axisLabel: { color: textColor, fontSize: 9 }, axisLine: { lineStyle: { color: axisLineColor } }, splitLine: { show: false } },
    ],
    series: [
      // Candlestick
      {
        name: 'OHLC',
        type: 'candlestick',
        xAxisIndex: 0,
        yAxisIndex: 0,
        data: toEChartsCandlestick(klines),
        itemStyle: { color: '#22c55e', color0: '#ef4444', borderColor: '#22c55e', borderColor0: '#ef4444' },
      },
      // SMA 20
      ...(ind.sma_20 ? [seriesFromIndicator('SMA 20', ind.sma_20, '#f59e0b')] : []),
      // Bollinger Bands
      ...(ind.bb_upper_20_2 ? [{ ...seriesFromIndicator('BB Upper', ind.bb_upper_20_2, '#818cf8'), lineStyle: { width: 1, type: 'dashed', color: '#818cf8' } }] : []),
      ...(ind.bb_middle_20 ? [{ ...seriesFromIndicator('BB Mid', ind.bb_middle_20, '#a78bfa'), lineStyle: { width: 1, type: 'dashed', color: '#a78bfa' } }] : []),
      ...(ind.bb_lower_20_2 ? [{ ...seriesFromIndicator('BB Lower', ind.bb_lower_20_2, '#818cf8'), lineStyle: { width: 1, type: 'dashed', color: '#818cf8' } }] : []),
      // Volume
      {
        name: 'Volume',
        type: 'bar',
        xAxisIndex: 1,
        yAxisIndex: 1,
        data: volumeData,
        barMaxWidth: 6,
      },
      // RSI
      ...(ind.rsi_14 ? [{
        name: 'RSI 14',
        type: 'line',
        xAxisIndex: 2,
        yAxisIndex: 2,
        data: (ind.rsi_14 as IndicatorPoint[]).map((p) => [p.time, p.value]),
        smooth: true,
        symbol: 'none',
        lineStyle: { width: 1.5, color: '#06b6d4' },
        markLine: {
          silent: true,
          lineStyle: { color: textColor, type: 'dashed', width: 0.8 },
          data: [{ yAxis: 70 }, { yAxis: 30 }],
          label: { show: true, formatter: '{c}', fontSize: 8, color: textColor },
        },
      }] : []),
    ],
  };

  // ── MACD chart ─────────────────────────────────────────────────────────────

  const macdOption = {
    backgroundColor: bgColor,
    animation: false,
    tooltip: {
      trigger: 'axis' as const,
      backgroundColor: isDark ? '#1f2937' : '#fff',
      textStyle: { color: textColor, fontSize: 11 },
    },
    legend: {
      top: 4,
      textStyle: { color: textColor, fontSize: 10 },
      itemWidth: 16,
      itemHeight: 8,
    },
    grid: { left: 60, right: 12, top: 28, bottom: 28 },
    xAxis: { type: 'category', data: times, axisLabel: { color: textColor, fontSize: 9 }, axisLine: { lineStyle: { color: axisLineColor } }, splitLine: { lineStyle: { color: gridColor } } },
    yAxis: { scale: true, axisLabel: { color: textColor, fontSize: 9 }, axisLine: { lineStyle: { color: axisLineColor } }, splitLine: { lineStyle: { color: gridColor } } },
    series: [
      ...(ind.macd_hist ? [{
        name: 'MACD Hist',
        type: 'bar',
        data: (ind.macd_hist as IndicatorPoint[]).map((p) => ({
          value: [p.time, p.value],
          itemStyle: { color: p.value >= 0 ? '#22c55e' : '#ef4444', opacity: 0.7 },
        })),
        barMaxWidth: 4,
      }] : []),
      ...(ind.macd_line ? [{ ...seriesFromIndicator('MACD', ind.macd_line, '#6366f1'), xAxisIndex: 0, yAxisIndex: 0 }] : []),
      ...(ind.macd_signal ? [{ ...seriesFromIndicator('Signal', ind.macd_signal, '#f97316'), xAxisIndex: 0, yAxisIndex: 0 }] : []),
    ],
  };

  // ── KD + OBV ──────────────────────────────────────────────────────────────

  const kdOption = {
    backgroundColor: bgColor,
    animation: false,
    tooltip: { trigger: 'axis' as const, backgroundColor: isDark ? '#1f2937' : '#fff', textStyle: { color: textColor, fontSize: 11 } },
    legend: { top: 4, textStyle: { color: textColor, fontSize: 10 }, itemWidth: 16, itemHeight: 8 },
    grid: { left: 60, right: 12, top: 28, bottom: 28 },
    xAxis: { type: 'category', data: times, axisLabel: { color: textColor, fontSize: 9 }, axisLine: { lineStyle: { color: axisLineColor } }, splitLine: { lineStyle: { color: gridColor } } },
    yAxis: { scale: true, axisLabel: { color: textColor, fontSize: 9 }, axisLine: { lineStyle: { color: axisLineColor } }, splitLine: { lineStyle: { color: gridColor } } },
    series: [
      ...(ind.kd_k ? [seriesFromIndicator('K', ind.kd_k, '#22c55e')] : []),
      ...(ind.kd_d ? [seriesFromIndicator('D', ind.kd_d, '#ef4444')] : []),
      ...(ind.kd_j ? [seriesFromIndicator('J', ind.kd_j, '#f59e0b')] : []),
    ],
  };

  const dmiOption = {
    backgroundColor: bgColor,
    animation: false,
    tooltip: { trigger: 'axis' as const, backgroundColor: isDark ? '#1f2937' : '#fff', textStyle: { color: textColor, fontSize: 11 } },
    legend: { top: 4, textStyle: { color: textColor, fontSize: 10 }, itemWidth: 16, itemHeight: 8 },
    grid: { left: 60, right: 12, top: 28, bottom: 28 },
    xAxis: { type: 'category', data: times, axisLabel: { color: textColor, fontSize: 9 }, axisLine: { lineStyle: { color: axisLineColor } }, splitLine: { lineStyle: { color: gridColor } } },
    yAxis: { scale: true, axisLabel: { color: textColor, fontSize: 9 }, axisLine: { lineStyle: { color: axisLineColor } }, splitLine: { lineStyle: { color: gridColor } } },
    series: [
      ...(ind.dmi_plus_di ? [seriesFromIndicator('+DI', ind.dmi_plus_di, '#22c55e')] : []),
      ...(ind.dmi_minus_di ? [seriesFromIndicator('-DI', ind.dmi_minus_di, '#ef4444')] : []),
      ...(ind.dmi_adx ? [seriesFromIndicator('ADX', ind.dmi_adx, '#f59e0b')] : []),
    ],
  };

  const inputClass = `px-3 py-1.5 rounded-lg text-xs border focus:outline-none focus:ring-1 focus:ring-app-accent appearance-none
    ${isDark ? 'bg-gray-800 border-gray-700 text-gray-100' : 'bg-white border-gray-200 text-gray-900'}`;

  return (
    <div className="p-6 space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div className="flex items-center space-x-3">
          <div className="p-2 rounded-lg bg-app-accent/10">
            <CandlestickChart size={18} className="text-app-accent" />
          </div>
          <div>
            <h1 className="text-lg font-bold text-app-text-main">Technical Analysis</h1>
            <p className="text-xs text-app-text-muted">Live Binance market data · SMA · BB · RSI · MACD · KD · DMI · OBV</p>
          </div>
        </div>
        <div className="flex items-center space-x-2">
          {/* Symbol selector */}
          <div className="relative">
            <select value={symbol} onChange={(e) => setSymbol(e.target.value)} className={inputClass}>
              {SYMBOLS.map((s) => <option key={s} value={s}>{s}</option>)}
            </select>
            <ChevronDown size={10} className="absolute right-2 top-2 text-app-text-muted pointer-events-none" />
          </div>
          {/* Interval selector */}
          <div className="relative">
            <select value={interval} onChange={(e) => setInterval(e.target.value)} className={inputClass}>
              {INTERVALS.map((iv) => <option key={iv} value={iv}>{iv}</option>)}
            </select>
            <ChevronDown size={10} className="absolute right-2 top-2 text-app-text-muted pointer-events-none" />
          </div>
          <button
            onClick={() => { void fetchData(); }}
            className="p-1.5 rounded-lg border border-app-border hover:bg-app-accent/10 text-app-text-muted hover:text-app-accent transition-colors"
            title="Refresh"
          >
            <RefreshCw size={13} className={loading ? 'animate-spin' : ''} />
          </button>
        </div>
      </div>

      {/* Error */}
      {error && (
        <div className="flex items-center space-x-2 px-4 py-3 rounded-lg bg-red-500/10 text-red-500 text-xs border border-red-500/20">
          <AlertTriangle size={14} />
          <span>{error}</span>
        </div>
      )}

      {/* Loading placeholder */}
      {loading && (
        <div className="flex items-center justify-center py-24 text-app-text-muted">
          <Loader size={24} className="animate-spin mr-3" />
          <span className="text-sm">Fetching {symbol} {interval} data…</span>
        </div>
      )}

      {!loading && klines.length > 0 && (
        <div className="space-y-4">
          {/* Main candlestick + RSI */}
          <ChartCard title={`${symbol} K-Line · SMA20 · Bollinger Bands · Volume · RSI(14)`} isDark={isDark}>
            <ReactECharts option={candlestickOption} style={{ height: 480 }} theme={isDark ? 'dark' : undefined} />
          </ChartCard>

          <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
            {/* MACD */}
            <ChartCard title="MACD (12, 26, 9)" isDark={isDark}>
              <ReactECharts option={macdOption} style={{ height: 220 }} theme={isDark ? 'dark' : undefined} />
            </ChartCard>

            {/* KD */}
            <ChartCard title="KD (9)" isDark={isDark}>
              <ReactECharts option={kdOption} style={{ height: 220 }} theme={isDark ? 'dark' : undefined} />
            </ChartCard>

            {/* DMI / ADX */}
            <ChartCard title="DMI / ADX (14)" isDark={isDark}>
              <ReactECharts option={dmiOption} style={{ height: 220 }} theme={isDark ? 'dark' : undefined} />
            </ChartCard>

            {/* OBV */}
            {ind.obv && (
              <ChartCard title="OBV (On-Balance Volume)" isDark={isDark}>
                <ReactECharts
                  option={{
                    backgroundColor: bgColor,
                    animation: false,
                    tooltip: { trigger: 'axis' as const, backgroundColor: isDark ? '#1f2937' : '#fff', textStyle: { color: textColor, fontSize: 11 } },
                    grid: { left: 72, right: 12, top: 16, bottom: 28 },
                    xAxis: { type: 'category', data: times, axisLabel: { color: textColor, fontSize: 9 }, axisLine: { lineStyle: { color: axisLineColor } }, splitLine: { lineStyle: { color: gridColor } } },
                    yAxis: { scale: true, axisLabel: { color: textColor, fontSize: 9, formatter: (v: number) => v >= 1_000_000 ? `${(v / 1_000_000).toFixed(1)}M` : v >= 1000 ? `${(v / 1000).toFixed(0)}K` : String(v) }, axisLine: { lineStyle: { color: axisLineColor } }, splitLine: { lineStyle: { color: gridColor } } },
                    series: [seriesFromIndicator('OBV', ind.obv as IndicatorPoint[], '#06b6d4')],
                  }}
                  style={{ height: 220 }}
                  theme={isDark ? 'dark' : undefined}
                />
              </ChartCard>
            )}

            {/* BIAS */}
            {ind.bias_20 && (
              <ChartCard title="BIAS(20)" isDark={isDark}>
                <ReactECharts
                  option={{
                    backgroundColor: bgColor,
                    animation: false,
                    tooltip: { trigger: 'axis' as const, backgroundColor: isDark ? '#1f2937' : '#fff', textStyle: { color: textColor, fontSize: 11 } },
                    grid: { left: 60, right: 12, top: 16, bottom: 28 },
                    xAxis: { type: 'category', data: times, axisLabel: { color: textColor, fontSize: 9 }, axisLine: { lineStyle: { color: axisLineColor } }, splitLine: { lineStyle: { color: gridColor } } },
                    yAxis: { scale: true, axisLabel: { color: textColor, fontSize: 9 }, axisLine: { lineStyle: { color: axisLineColor } }, splitLine: { lineStyle: { color: gridColor } } },
                    series: [seriesFromIndicator('BIAS', ind.bias_20 as IndicatorPoint[], '#a855f7')],
                  }}
                  style={{ height: 220 }}
                  theme={isDark ? 'dark' : undefined}
                />
              </ChartCard>
            )}

            {/* A/D Line */}
            {ind.ad && (
              <ChartCard title="A/D Line (Accumulation/Distribution)" isDark={isDark}>
                <ReactECharts
                  option={{
                    backgroundColor: bgColor,
                    animation: false,
                    tooltip: { trigger: 'axis' as const, backgroundColor: isDark ? '#1f2937' : '#fff', textStyle: { color: textColor, fontSize: 11 } },
                    grid: { left: 72, right: 12, top: 16, bottom: 28 },
                    xAxis: { type: 'category', data: times, axisLabel: { color: textColor, fontSize: 9 }, axisLine: { lineStyle: { color: axisLineColor } }, splitLine: { lineStyle: { color: gridColor } } },
                    yAxis: { scale: true, axisLabel: { color: textColor, fontSize: 9, formatter: (v: number) => v >= 1_000_000 ? `${(v / 1_000_000).toFixed(1)}M` : String(v) }, axisLine: { lineStyle: { color: axisLineColor } }, splitLine: { lineStyle: { color: gridColor } } },
                    series: [seriesFromIndicator('A/D', ind.ad as IndicatorPoint[], '#ec4899')],
                  }}
                  style={{ height: 220 }}
                  theme={isDark ? 'dark' : undefined}
                />
              </ChartCard>
            )}
          </div>
        </div>
      )}

      {!loading && klines.length === 0 && !error && (
        <div className="flex flex-col items-center justify-center py-24 text-app-text-muted space-y-2">
          <CandlestickChart size={32} className="opacity-30" />
          <span className="text-sm">Select a symbol and click refresh to load data.</span>
        </div>
      )}
    </div>
  );
}

// ── Helper ────────────────────────────────────────────────────────────────────

function ChartCard({ title, isDark, children }: { title: string; isDark: boolean; children: ReactNode }) {
  return (
    <div className={`rounded-xl border overflow-hidden ${isDark ? 'bg-gray-900 border-gray-800' : 'bg-white border-gray-200'}`}>
      <div className={`px-4 py-2.5 border-b text-xs font-semibold text-app-text-muted ${isDark ? 'border-gray-800' : 'border-gray-200'}`}>
        {title}
      </div>
      {children}
    </div>
  );
}
