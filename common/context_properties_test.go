package common_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/inkly/CasaOS-AppManagement/common"
	"gotest.tools/v3/assert"
)

// Reading the properties while another goroutine adds to them is the normal shape of
// this code, not an edge case: every `go PublishEventWrapper(ctx, ...)` ranges over the
// map, and a recreate writes `app:updated` at the very end, with those goroutines still
// running. Unguarded that is `fatal error: concurrent map read and map write`, which no
// recover() catches -- the daemon dies and every app on the box stops being managed.
//
// The runtime's own map checker reports that without -race, so this is a real check
// wherever it runs.
func TestPropertiesSurviveConcurrentReadAndWrite(t *testing.T) {
	ctx := common.WithProperties(context.Background(), map[string]string{"app:name": "gluetun-stack"})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)

		go func(i int) {
			defer wg.Done()
			common.SetProperties(ctx, map[string]string{fmt.Sprintf("property:%d", i): "set"})
		}(i)

		go func() {
			defer wg.Done()
			for range common.PropertiesFromContext(ctx) { //nolint:revive // ranging it is the point
			}
		}()
	}
	wg.Wait()

	properties := common.PropertiesFromContext(ctx)
	assert.Equal(t, len(properties), 51)
	assert.Equal(t, properties["app:name"], "gluetun-stack")

	// the copy is a copy: writing to it must not reach the events
	properties["app:name"] = "not-this"
	assert.Equal(t, common.PropertiesFromContext(ctx)["app:name"], "gluetun-stack")

	// a context that carries no properties is not a crash, it is a context that
	// publishes no events of its own
	common.SetProperties(context.Background(), map[string]string{"app:name": "nowhere"})
	assert.Equal(t, len(common.PropertiesFromContext(context.Background())), 0)
}
