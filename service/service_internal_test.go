package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/config"
	"github.com/ReCasaOS/CasaOS-Common/external"
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
