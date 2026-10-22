package collect

import (
	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/snapshot"
)

// Status reports collection progress for the readiness endpoint (§7.6).
//
// Attempted and Collected are kept separate so readiness can tell "has not collected
// yet" from "collected and now failing", which are different operational states
// (P6). An unattempted plan must not look like a plan reporting zero.
type Status struct {
	Plans int
	// Attempted counts plans that have finished at least one collection attempt.
	Attempted int
	// Collected counts plans that have published at least one successful result.
	Collected int
}

// Ready reports whether every plan has published at least once.
func (s Status) Ready() bool { return s.Collected == s.Plans }

// Status returns a point-in-time view of collection progress.
func (s *Scheduler) Status() Status {
	s.mu.Lock()
	runners := s.runners
	s.mu.Unlock()

	st := Status{Plans: len(runners)}
	for _, r := range runners {
		if r.attempted.Load() {
			st.Attempted++
		}
		if r.collected.Load() {
			st.Collected++
		}
	}
	return st
}

// StoreConfigFor derives store configuration from compiled plans.
//
// Translates config.Failure into snapshot.Policy here rather than in the store,
// because internal/snapshot deliberately depends on no other internal package.
// MaxTotalSeries is global, so the strictest plan limit wins.
func StoreConfigFor(plans []Plan) snapshot.StoreConfig {
	cfg := snapshot.StoreConfig{Policies: make(map[string]snapshot.Policy, len(plans))}
	for _, p := range plans {
		cfg.Policies[p.ID] = policyFor(p.Failure)
		if n := p.Limits.MaxTotalSeries; n > 0 && (cfg.MaxTotalSeries == 0 || n < cfg.MaxTotalSeries) {
			cfg.MaxTotalSeries = n
		}
	}
	return cfg
}

func policyFor(f config.Failure) snapshot.Policy {
	p := snapshot.Policy{MaxStaleAge: f.MaxStaleAge.Duration()}
	if f.Mode == config.FailureDrop {
		p.Mode = snapshot.Drop
		return p
	}
	p.Mode = snapshot.Retain
	return p
}
