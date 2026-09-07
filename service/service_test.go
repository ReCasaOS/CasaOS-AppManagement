package service_test

const (
	topFunc1  = "go.opencensus.io/stats/view.(*worker).start"
	pollFunc1 = "internal/poll.runtime_pollWait"
	httpFunc1 = "net/http.(*persistConn).writeLoop"
	// CasaOS-Common/external pulls orca-zhang/ecache, whose init() starts a
	// clock goroutine that never returns.
	ecacheClock = "github.com/orca-zhang/ecache.init.0.func1"
)
