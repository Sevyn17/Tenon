package memstore

import (
	"testing"

	"ybase/runtime/store"
	"ybase/runtime/store/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		s := New()
		t.Cleanup(func() { s.Close() })
		return s
	})
}
