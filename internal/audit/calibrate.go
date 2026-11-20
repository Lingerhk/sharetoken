package audit

import (
	"context"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	"sharetoken/internal/coordinator"
)

// Calibration. An honest node answers exactly as the reference does when
// asked the same question, so the reference scoring its own answers gives
// the scores honest nodes get. Their spread differs per model and per
// provider (a hosted model is not perfectly deterministic even at
// temperature 0), so thresholds are measured, not fixed: the threshold is
// the 0.1th percentile of honest K-question means, less a margin.

// CalibrateOptions tunes a calibration run.
type CalibrateOptions struct {
	Samples int     // questions answered and scored (default 40)
	Margin  float64 // subtracted from the honest 0.1th percentile (default 0.02)
	// Drift is how far the honest median may move from the previous
	// calibration before the model's audits go to shadow (default 0.05).
	Drift float64
	// Parallel is how many questions run at once (default 4).
	Parallel int
}

// Calibrate measures the honest score distribution of model's reference and
// stores it; audits of the model use its threshold from then on.
func (a *Auditor) Calibrate(ctx context.Context, model string, o CalibrateOptions) (*coordinator.AuditCalibration, error) {
	ref, ok := a.refs[model]
	if !ok {
		return nil, fmt.Errorf("audit: no reference for %q", model)
	}
	if ref.Verifier == nil || ref.Verifier.Method() == MethodNone {
		return nil, fmt.Errorf("audit: %s: the reference cannot score answers (verify: none)", model)
	}
	if o.Samples <= 0 {
		o.Samples = 40
	}
	if o.Margin <= 0 {
		o.Margin = 0.02
	}
	if o.Drift <= 0 {
		o.Drift = 0.05
	}
	if o.Parallel <= 0 {
		o.Parallel = 4
	}
	c := &coordinator.AuditCalibration{Model: model, Method: ref.Verifier.Method()}
	type outcome struct {
		s   *Score
		err error
	}
	outs := make([]outcome, o.Samples)
	sem := make(chan struct{}, o.Parallel)
	var wg sync.WaitGroup
	for i := range outs {
		ch := a.challenge()
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			q := Question{Messages: ch.messages(), MaxTokens: a.cfg.MaxTokens}
			text, stopped, err := ref.Verifier.Generate(ctx, q)
			if err != nil {
				outs[i].err = err
				return
			}
			if strings.TrimSpace(text) == "" {
				outs[i].err = errors.New("the reference gave an empty answer (does it think by default? set its params)")
				return
			}
			outs[i].s, outs[i].err = ref.Verifier.Score(ctx, q, text, stopped, a.cfg.ScoreTokens)
		})
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var top1 []float64
	fails, calls, errs := 0, 0, 0
	var lastErr error
	for _, out := range outs {
		if out.err != nil {
			errs++
			lastErr = out.err
			continue
		}
		calls += out.s.Calls + 1
		top1 = append(top1, out.s.Top1)
		if out.s.Fail {
			fails++
		}
	}
	if errs > o.Samples/4 {
		return nil, fmt.Errorf("audit: calibration of %s: %d of %d questions failed: %w", model, errs, o.Samples, lastErr)
	}
	if len(top1) < 10 {
		return nil, errors.New("audit: calibration needs at least 10 scored questions")
	}
	c.Samples = len(top1)
	c.FailRate = float64(fails) / float64(len(top1))
	if c.Method == MethodTF {
		r := mrand.New(mrand.NewPCG(1, 2)) // reproducible from the same scores
		means := kMeans(top1, a.cfg.Questions, 20000, r)
		c.HonestMean = mean(top1)
		c.HonestMedian = quantile(means, 0.5)
		c.HonestP001 = quantile(means, 0.001)
		c.Threshold = min(c.HonestP001-o.Margin, 0.97)
		sorted := slices.Sorted(slices.Values(top1))
		c.Detail = fmt.Sprintf("%d questions, %d reference calls; per question top-1 min %.3f median %.3f; %d-question means: median %.3f, p0.1 %.3f",
			len(top1), calls, sorted[0], quantile(sorted, 0.5), a.cfg.Questions, c.HonestMedian, c.HonestP001)
	} else {
		c.Detail = fmt.Sprintf("%d questions, %d reference calls; honest answers failing the divergence check: %.1f%%", len(top1), calls, 100*c.FailRate)
	}
	if prev, err := a.eng.LatestCalibration(model); err == nil && prev != nil && prev.Method == c.Method {
		moved := c.HonestMedian - prev.HonestMedian
		if c.Method == MethodDiv {
			moved = c.FailRate - prev.FailRate
		}
		if moved > o.Drift || moved < -o.Drift {
			c.Drift = true
			c.Detail += fmt.Sprintf("; DRIFT: moved %+.3f since %s", moved, time.Unix(0, prev.At).UTC().Format(time.RFC3339))
			a.log.Warn("reference model drifted: its audits are recorded without penalty until the next calibration", "model", model, "moved", moved)
		}
	}
	if err := a.eng.SaveCalibration(*c); err != nil {
		return nil, err
	}
	a.log.Info("audit calibration", "model", model, "method", c.Method, "threshold", fmt.Sprintf("%.3f", c.Threshold), "detail", c.Detail)
	return c, nil
}

// CalibrateLoop recalibrates every scoring reference now and then every
// `every` until ctx ends.
func (a *Auditor) CalibrateLoop(ctx context.Context, every time.Duration, o CalibrateOptions) {
	for {
		for model, ref := range a.refs {
			if ref.Verifier == nil || ref.Verifier.Method() == MethodNone {
				continue
			}
			if _, err := a.Calibrate(ctx, model, o); err != nil && ctx.Err() == nil {
				a.log.Warn("audit calibration failed", "model", model, "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}
