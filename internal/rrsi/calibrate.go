package rrsi

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
)

// CaseTrials is one evaluation case's repeated outcomes under one unchanged
// configuration. Each entry is whether that repetition passed.
type CaseTrials struct {
	CaseID   string
	Outcomes []bool
}

// Calibration is a measured noise band and how it was measured.
type Calibration struct {
	Band   float64
	Method string
	// Cases and Trials are what it was measured over, because a band from
	// three trials and a band from three hundred are different claims and the
	// number is the only thing that says which.
	Cases, Trials int
	// Spread is the standard error of the score estimate before the safety
	// factor, kept so a reader can see what the band is made of.
	Spread float64
}

// minCalibrationRepetitions is how many times a case must have been repeated
// before its spread means anything.
//
// Two, because a single run of each case tells you the score and nothing at
// all about how much that score moves. One number cannot have a spread.
const minCalibrationRepetitions = 2

// safetyFactor widens the measured standard error into a band.
//
// Two standard errors, so a difference inside the band is one the unchanged
// configuration produces often enough that calling it an improvement would be
// wrong most of the times it happened.
const safetyFactor = 2.0

// Calibrate measures how much the score of an unchanged configuration moves.
//
// It resamples the repetitions within each case, which is where the variance
// actually is: the same configuration on the same case does not always do the
// same thing, and the score is an average over cases of exactly that.
//
// It refuses rather than returning a small number when the repetitions are not
// there. A band estimated from one run per case would be zero, and a zero band
// says every difference is real — the opposite of what calibration is for, and
// it would arrive wearing the authority of a measurement.
func Calibrate(cases []CaseTrials, resamples int, seed int64) (Calibration, error) {
	var calibration Calibration
	usable := make([]CaseTrials, 0, len(cases))
	trials := 0
	for _, entry := range cases {
		if len(entry.Outcomes) >= minCalibrationRepetitions {
			usable = append(usable, entry)
			trials += len(entry.Outcomes)
		}
	}
	if len(usable) == 0 {
		return calibration, fmt.Errorf("%w: 같은 구성으로 각 사례를 최소 %d회 반복해야 측정값이 얼마나 흔들리는지 알 수 있습니다",
			ErrNotCalibrated, minCalibrationRepetitions)
	}
	if resamples <= 0 {
		resamples = 2000
	}
	sort.SliceStable(usable, func(i, j int) bool { return usable[i].CaseID < usable[j].CaseID })
	random := rand.New(rand.NewSource(seed))
	scores := make([]float64, 0, resamples)
	for i := 0; i < resamples; i++ {
		passed, total := 0.0, 0.0
		for _, entry := range usable {
			for range entry.Outcomes {
				if entry.Outcomes[random.Intn(len(entry.Outcomes))] {
					passed++
				}
				total++
			}
		}
		if total > 0 {
			scores = append(scores, passed/total)
		}
	}
	calibration.Spread = populationStdDev(scores)
	calibration.Band = calibration.Spread * safetyFactor
	calibration.Cases, calibration.Trials = len(usable), trials
	calibration.Method = fmt.Sprintf("사례 %d개 · 시행 %d회 재표본 추출 (표준오차 %s × %.0f)",
		calibration.Cases, calibration.Trials, percent(calibration.Spread), safetyFactor)
	if calibration.Band <= 0 {
		// Every repetition of every case agreed. That is a real result and it
		// is not a measured band: nothing has been shown about how much the
		// score moves, only that it did not move this time.
		return calibration, fmt.Errorf("%w: 반복이 모두 같은 결과라 흔들림을 측정할 수 없었습니다 — 사례를 더 넣거나 더 반복하세요",
			ErrNotCalibrated)
	}
	return calibration, nil
}

func populationStdDev(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	mean := 0.0
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	sum := 0.0
	for _, value := range values {
		sum += (value - mean) * (value - mean)
	}
	return math.Sqrt(sum / float64(len(values)))
}
