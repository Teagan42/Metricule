package mapping_test

import (
	"sync"
	"testing"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/source"
)

// A compiled mapper is shared by concurrent collections, so Map must hold no state.
func TestMapperIsSafeForConcurrentUse(t *testing.T) {
	mapper := mustCompile(t, gaugeSpec(config.NullFail))
	rs := source.ResultSet{Rows: []source.Row{
		{"status": "pending", "value": int64(12)},
		{"status": "running", "value": int64(4)},
	}}

	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			fam, err := mapper.Map(rs)
			if err != nil {
				t.Errorf("Map: %v", err)
				return
			}
			if len(fam.Samples) != 2 {
				t.Errorf("samples = %d, want 2", len(fam.Samples))
			}
		})
	}
	wg.Wait()
}
