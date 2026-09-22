package service

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/config"
	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"gotest.tools/v3/assert"
)

// The message bus refuses a loopback call without this boot's internal secret,
// so the fallback publish path has to send it.
func TestMessageBusClientSendsTheInternalSecret(t *testing.T) {
	var authorization string
	bus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
	}))
	defer bus.Close()

	setRuntimePathForTest(t)
	runtime := config.CommonInfo.RuntimePath
	assert.NilError(t, os.WriteFile(filepath.Join(runtime, external.InternalSecretFilename), []byte("s3cret"), 0o600))
	assert.NilError(t, os.WriteFile(filepath.Join(runtime, external.MessageBusAddressFilename), []byte(bus.URL), 0o600))

	response, err := (&store{}).MessageBus().PublishEventWithResponse(context.Background(), common.AppManagementServiceName, "test", map[string]string{})
	assert.NilError(t, err)
	assert.Equal(t, response.StatusCode(), http.StatusOK)
	assert.Equal(t, authorization, "Internal s3cret")
}

// An event outlives what caused it: most are published from goroutines after the
// HTTP request that started the operation has been answered, and a scheduled
// backup cut short by a shutdown still reports its error. A cancelled context
// must not stop the publish, over the socket or the address file it falls back to.
func TestPublishOutlivesItsContext(t *testing.T) {
	logger.LogInitConsoleOnly()

	previous := MyService
	MyService = &store{}
	t.Cleanup(func() { MyService = previous })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for _, overSocket := range []bool{true, false} {
		t.Run(map[bool]string{true: "socket", false: "fallback"}[overSocket], func(t *testing.T) {
			setRuntimePathForTest(t)
			runtime := config.CommonInfo.RuntimePath

			paths := make(chan string, 1)
			bus := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths <- r.URL.Path
			}))
			if overSocket {
				listener, err := net.Listen("unix", external.MessageBusSocketPath(runtime))
				assert.NilError(t, err)
				bus.Listener.Close()
				bus.Listener = listener
			}
			bus.Start()
			defer bus.Close()
			if !overSocket {
				assert.NilError(t, os.WriteFile(filepath.Join(runtime, external.MessageBusAddressFilename), []byte(bus.URL), 0o600))
			}

			PublishEventWrapper(ctx, common.EventTypeAppInstallBegin, nil)

			select {
			case path := <-paths:
				assert.Equal(t, path, "/v2/message_bus/event/"+common.AppManagementServiceName+"/"+common.EventTypeAppInstallBegin.Name)
			default:
				t.Fatal("the event never reached the message bus")
			}
		})
	}
}
