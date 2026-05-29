package service

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"server/domain"
	"server/model/repo"
	"server/model/store"
)

// IndicatorPoint is a single timestamped indicator value
type IndicatorPoint struct {
	At    time.Time `json:"at"`
	Value float64   `json:"value"`
}

func (p IndicatorPoint) MarshalJSON() ([]byte, error) {
	type payload struct {
		At    time.Time `json:"at"`
		Time  time.Time `json:"time"`
		Value float64   `json:"value"`
	}
	return json.Marshal(payload{At: p.At, Time: p.At, Value: p.Value})
}

// IndicatorService computes common technical indicators from replay klines
type IndicatorService struct {
	repo *repo.Repository
}

func NewIndicatorService(r *repo.Repository) *IndicatorService {
	return &IndicatorService{repo: r}
}

// ComputeForDataset computes a minimal set of indicators for the dataset up to the given time.
// It returns a map[indicatorName] -> []IndicatorPoint (ordered by time asc).
func (s *IndicatorService) ComputeForDataset(ctx context.Context, datasetID string, until time.Time) (map[string][]IndicatorPoint, error) {
	var klines []store.ReplayKline
	if err := s.repo.WithContext(ctx).
		Where("dataset_id = ? AND ts <= ?", datasetID, until).
		Order("ts desc").
		Limit(500).
		Find(&klines).Error; err != nil {
		return nil, err
	}
	for i, j := 0, len(klines)-1; i < j; i, j = i+1, j-1 {
		klines[i], klines[j] = klines[j], klines[i]
	}

	n := len(klines)
	if n == 0 {
		return map[string][]IndicatorPoint{}, nil
	}

	highs := make([]float64, n)
	lows := make([]float64, n)
	closes := make([]float64, n)
	volumes := make([]float64, n)
	times := make([]time.Time, n)
	for i := range n {
		highs[i] = klines[i].High
		lows[i] = klines[i].Low
		closes[i] = klines[i].Close
		volumes[i] = klines[i].Volume
		times[i] = klines[i].Ts
	}

	return computeIndicatorsFromArrays(times, highs, lows, closes, volumes), nil
}

// ComputeFromKlines computes the full indicator set from any []domain.Kline slice (e.g. live Binance data).
// Returns an empty map (no error) when klines is empty.
func (s *IndicatorService) ComputeFromKlines(klines []domain.Kline) (map[string][]IndicatorPoint, error) {
	n := len(klines)
	if n == 0 {
		return map[string][]IndicatorPoint{}, nil
	}
	highs := make([]float64, n)
	lows := make([]float64, n)
	closes := make([]float64, n)
	volumes := make([]float64, n)
	times := make([]time.Time, n)
	for i, k := range klines {
		highs[i] = k.High
		lows[i] = k.Low
		closes[i] = k.Close
		volumes[i] = k.Volume
		times[i] = k.At
	}
	return computeIndicatorsFromArrays(times, highs, lows, closes, volumes), nil
}

// computeIndicatorsFromArrays is the shared core that both ComputeForDataset and ComputeFromKlines delegate to.
func computeIndicatorsFromArrays(times []time.Time, highs, lows, closes, volumes []float64) map[string][]IndicatorPoint {
	n := len(times)
	out := map[string][]IndicatorPoint{}

	out["sma_20"] = pointsFromSMASeries(times, smaSeries(closes, 20))

	ema12 := emaSeries(closes, 12)
	ema26 := emaSeries(closes, 26)
	macdLine := make([]float64, n)
	for i := range n {
		if isFinite(ema12[i]) && isFinite(ema26[i]) {
			macdLine[i] = ema12[i] - ema26[i]
		} else {
			macdLine[i] = math.NaN()
		}
	}
	macdSignal := emaSeries(macdLine, 9)
	macdHist := make([]float64, n)
	for i := range n {
		if isFinite(macdLine[i]) && isFinite(macdSignal[i]) {
			macdHist[i] = macdLine[i] - macdSignal[i]
		} else {
			macdHist[i] = math.NaN()
		}
	}
	out["macd_line"] = pointsFromSeries(times, macdLine)
	out["macd_signal"] = pointsFromSeries(times, macdSignal)
	out["macd_hist"] = pointsFromSeries(times, macdHist)

	out["rsi_14"] = pointsFromSeries(times, rsiSeries(closes, 14))

	bbMiddle := smaSeries(closes, 20)
	out["bb_upper_20_2"] = pointsFromMap(times, bbUpperSeries(closes, 20, 2))
	out["bb_middle_20"] = pointsFromSMASeries(times, bbMiddle)
	out["bb_lower_20_2"] = pointsFromMap(times, bbLowerSeries(closes, 20, 2))

	out["obv"] = pointsFromSeries(times, obvSeries(closes, volumes))

	kdK, kdD, kdJ := kdSeries(highs, lows, closes, 9)
	out["kd_k"] = pointsFromSeries(times, kdK)
	out["kd_d"] = pointsFromSeries(times, kdD)
	out["kd_j"] = pointsFromSeries(times, kdJ)

	plusDI, minusDI, adx := dmiSeries(highs, lows, closes, 14)
	out["dmi_plus_di"] = pointsFromSeries(times, plusDI)
	out["dmi_minus_di"] = pointsFromSeries(times, minusDI)
	out["dmi_adx"] = pointsFromSeries(times, adx)

	out["ad"] = pointsFromSeries(times, adSeries(highs, lows, closes, volumes))
	out["bias_20"] = pointsFromSeries(times, biasSeries(closes, 20))

	return out
}

type smaPoint struct {
	index int
	value float64
}

func smaSeries(values []float64, period int) []smaPoint {
	n := len(values)
	if period <= 0 || n == 0 || n < period {
		return nil
	}
	res := make([]smaPoint, 0, n-period+1)
	sum := 0.0
	for i := range n {
		sum += values[i]
		if i >= period {
			sum -= values[i-period]
		}
		if i >= period-1 {
			res = append(res, smaPoint{index: i, value: sum / float64(period)})
		}
	}
	return res
}

func emaSeries(values []float64, period int) []float64 {
	n := len(values)
	res := nanSlice(n)
	if period <= 0 || n == 0 {
		return res
	}

	k := 2.0 / (float64(period) + 1.0)
	seedSum := 0.0
	seedCount := 0
	prev := 0.0
	seeded := false
	for i, value := range values {
		if !isFinite(value) {
			continue
		}
		if !seeded {
			seedSum += value
			seedCount++
			if seedCount == period {
				prev = seedSum / float64(period)
				res[i] = prev
				seeded = true
			}
			continue
		}
		prev = value*k + prev*(1.0-k)
		res[i] = prev
	}
	return res
}

func rsiSeries(values []float64, period int) []float64 {
	n := len(values)
	res := nanSlice(n)
	if period <= 0 || n == 0 || n < period+1 {
		return res
	}
	gains := make([]float64, n)
	losses := make([]float64, n)
	for i := 1; i < n; i++ {
		diff := values[i] - values[i-1]
		if diff > 0 {
			gains[i] = diff
		} else {
			losses[i] = -diff
		}
	}
	avgGain := 0.0
	avgLoss := 0.0
	for i := 1; i <= period; i++ {
		avgGain += gains[i]
		avgLoss += losses[i]
	}
	avgGain /= float64(period)
	avgLoss /= float64(period)
	res[period] = rsiValue(avgGain, avgLoss)
	for i := period + 1; i < n; i++ {
		avgGain = (avgGain*float64(period-1) + gains[i]) / float64(period)
		avgLoss = (avgLoss*float64(period-1) + losses[i]) / float64(period)
		res[i] = rsiValue(avgGain, avgLoss)
	}
	return res
}

func rsiValue(avgGain, avgLoss float64) float64 {
	if avgLoss == 0 {
		if avgGain == 0 {
			return 50
		}
		return 100
	}
	rs := avgGain / avgLoss
	return 100.0 - 100.0/(1.0+rs)
}

func bbUpperSeries(values []float64, period int, k float64) map[int]float64 {
	n := len(values)
	out := map[int]float64{}
	if period <= 0 || n == 0 || n < period {
		return out
	}
	for i := period - 1; i < n; i++ {
		mean, std := rollingMeanStd(values, i-period+1, i)
		out[i] = mean + k*std
	}
	return out
}

func bbLowerSeries(values []float64, period int, k float64) map[int]float64 {
	n := len(values)
	out := map[int]float64{}
	if period <= 0 || n == 0 || n < period {
		return out
	}
	for i := period - 1; i < n; i++ {
		mean, std := rollingMeanStd(values, i-period+1, i)
		out[i] = mean - k*std
	}
	return out
}

func obvSeries(closes, volumes []float64) []float64 {
	n := len(closes)
	out := make([]float64, n)
	if n == 0 {
		return out
	}
	obv := 0.0
	out[0] = obv
	for i := 1; i < n; i++ {
		if closes[i] > closes[i-1] {
			obv += volumes[i]
		} else if closes[i] < closes[i-1] {
			obv -= volumes[i]
		}
		out[i] = obv
	}
	return out
}

func kdSeries(highs, lows, closes []float64, period int) ([]float64, []float64, []float64) {
	n := len(closes)
	kSeries := nanSlice(n)
	dSeries := nanSlice(n)
	jSeries := nanSlice(n)
	if period <= 0 || n < period {
		return kSeries, dSeries, jSeries
	}
	prevK := 50.0
	prevD := 50.0
	for i := period - 1; i < n; i++ {
		highest := highs[i-period+1]
		lowest := lows[i-period+1]
		for j := i - period + 2; j <= i; j++ {
			if highs[j] > highest {
				highest = highs[j]
			}
			if lows[j] < lowest {
				lowest = lows[j]
			}
		}
		rsv := 50.0
		if highest != lowest {
			rsv = (closes[i] - lowest) / (highest - lowest) * 100
		}
		k := prevK*2.0/3.0 + rsv/3.0
		d := prevD*2.0/3.0 + k/3.0
		j := 3*k - 2*d
		kSeries[i] = k
		dSeries[i] = d
		jSeries[i] = j
		prevK = k
		prevD = d
	}
	return kSeries, dSeries, jSeries
}

func dmiSeries(highs, lows, closes []float64, period int) ([]float64, []float64, []float64) {
	n := len(closes)
	plusDI := nanSlice(n)
	minusDI := nanSlice(n)
	adx := nanSlice(n)
	if period <= 0 || n <= period {
		return plusDI, minusDI, adx
	}

	tr := make([]float64, n)
	plusDM := make([]float64, n)
	minusDM := make([]float64, n)
	for i := 1; i < n; i++ {
		upMove := highs[i] - highs[i-1]
		downMove := lows[i-1] - lows[i]
		if upMove > downMove && upMove > 0 {
			plusDM[i] = upMove
		}
		if downMove > upMove && downMove > 0 {
			minusDM[i] = downMove
		}
		rangeHighLow := highs[i] - lows[i]
		rangeHighClose := math.Abs(highs[i] - closes[i-1])
		rangeLowClose := math.Abs(lows[i] - closes[i-1])
		tr[i] = math.Max(rangeHighLow, math.Max(rangeHighClose, rangeLowClose))
	}

	smoothedTR := 0.0
	smoothedPlusDM := 0.0
	smoothedMinusDM := 0.0
	for i := 1; i <= period; i++ {
		smoothedTR += tr[i]
		smoothedPlusDM += plusDM[i]
		smoothedMinusDM += minusDM[i]
	}
	for i := period; i < n; i++ {
		if i > period {
			smoothedTR = smoothedTR - smoothedTR/float64(period) + tr[i]
			smoothedPlusDM = smoothedPlusDM - smoothedPlusDM/float64(period) + plusDM[i]
			smoothedMinusDM = smoothedMinusDM - smoothedMinusDM/float64(period) + minusDM[i]
		}
		if smoothedTR == 0 {
			plusDI[i] = 0
			minusDI[i] = 0
			adx[i] = 0
			continue
		}
		plusDI[i] = 100 * smoothedPlusDM / smoothedTR
		minusDI[i] = 100 * smoothedMinusDM / smoothedTR
		dx := 0.0
		denom := plusDI[i] + minusDI[i]
		if denom != 0 {
			dx = math.Abs(plusDI[i]-minusDI[i]) / denom * 100
		}
		if i == period {
			adx[i] = dx
		} else {
			adx[i] = (adx[i-1]*float64(period-1) + dx) / float64(period)
		}
	}
	return plusDI, minusDI, adx
}

func adSeries(highs, lows, closes, volumes []float64) []float64 {
	n := len(closes)
	out := make([]float64, n)
	ad := 0.0
	for i := range n {
		multiplier := 0.0
		if highs[i] != lows[i] {
			multiplier = ((closes[i] - lows[i]) - (highs[i] - closes[i])) / (highs[i] - lows[i])
		}
		ad += multiplier * volumes[i]
		out[i] = ad
	}
	return out
}

func biasSeries(closes []float64, period int) []float64 {
	n := len(closes)
	out := nanSlice(n)
	for _, point := range smaSeries(closes, period) {
		if point.value != 0 {
			out[point.index] = (closes[point.index] - point.value) / point.value * 100
		}
	}
	return out
}

func rollingMeanStd(values []float64, start, end int) (float64, float64) {
	sum := 0.0
	for i := start; i <= end; i++ {
		sum += values[i]
	}
	mean := sum / float64(end-start+1)
	sdSum := 0.0
	for i := start; i <= end; i++ {
		delta := values[i] - mean
		sdSum += delta * delta
	}
	return mean, math.Sqrt(sdSum / float64(end-start+1))
}

func pointsFromSMASeries(times []time.Time, series []smaPoint) []IndicatorPoint {
	points := make([]IndicatorPoint, 0, len(series))
	for _, point := range series {
		if isFinite(point.value) {
			points = append(points, IndicatorPoint{At: times[point.index], Value: point.value})
		}
	}
	return points
}

func pointsFromSeries(times []time.Time, series []float64) []IndicatorPoint {
	points := make([]IndicatorPoint, 0, len(series))
	for i, value := range series {
		if isFinite(value) {
			points = append(points, IndicatorPoint{At: times[i], Value: value})
		}
	}
	return points
}

func pointsFromMap(times []time.Time, series map[int]float64) []IndicatorPoint {
	points := make([]IndicatorPoint, 0, len(series))
	for i := range len(times) {
		if value, ok := series[i]; ok && isFinite(value) {
			points = append(points, IndicatorPoint{At: times[i], Value: value})
		}
	}
	return points
}

func nanSlice(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = math.NaN()
	}
	return out
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
