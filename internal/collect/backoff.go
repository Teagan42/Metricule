package collect

import "time"

// Retry defaults. Bounded so a dead source produces a slow trickle of attempts
// rather than a query storm (P5, §7.3).
const (
	DefaultRetryAttempts = 3
	DefaultRetryBase     = time.Second
	DefaultRetryMax      = 30 * time.Second
	DefaultRetryFactor   = 2.0
)

// RetryPolicy bounds retries for a failed collection.
//
// Exhausting the attempts does not stop collection: the plan falls back to its
// normal schedule, which is what keeps retry cost bounded during a long outage.
type RetryPolicy struct {
	// MaxAttempts counts attempts per trigger, including the first. One disables
	// retries.
	MaxAttempts int
	Base        time.Duration
	Max         time.Duration
	Factor      float64
}

func (p RetryPolicy) withDefaults() RetryPolicy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = DefaultRetryAttempts
	}
	if p.Base <= 0 {
		p.Base = DefaultRetryBase
	}
	if p.Max <= 0 {
		p.Max = DefaultRetryMax
	}
	if p.Factor < 1 {
		p.Factor = DefaultRetryFactor
	}
	return p
}

// Delay returns the wait before attempt n, counted from 1 for the first retry.
func (p RetryPolicy) Delay(n int) time.Duration {
	d := float64(p.Base)
	for range max(n-1, 0) {
		d *= p.Factor
		if d >= float64(p.Max) {
			return p.Max
		}
	}
	return time.Duration(d)
}
