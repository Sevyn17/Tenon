package memstore

import (
	"testing"

	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/store/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		s := New()
		t.Cleanup(func() { s.Close() })
		return s
	})
}
