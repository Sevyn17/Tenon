package sqlite

import (
	"testing"

	"tenon/runtime/store"
	"tenon/runtime/store/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store { return openTest(t) })
}
