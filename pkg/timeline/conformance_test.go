package timeline_test

import (
	"testing"

	timeline "github.com/maomada/radar/pkg/timeline"
	"github.com/maomada/radar/pkg/timeline/storetest"
)

func TestMemoryStore_Conformance(t *testing.T) {
	storetest.RunConformance(t, func(t *testing.T) timeline.EventStore {
		return timeline.NewMemoryStore(100)
	})
}
