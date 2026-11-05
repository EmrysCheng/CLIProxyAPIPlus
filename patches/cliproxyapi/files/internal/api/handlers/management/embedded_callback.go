package management

import (
	"fmt"
	"sync/atomic"
)

var embeddedCallbacks atomic.Bool

func SetEmbeddedCallbacks(loopbackOnly bool) { embeddedCallbacks.Store(loopbackOnly) }
func callbackForwarderAddr(port int) string {
	host := "0.0.0.0"
	if embeddedCallbacks.Load() {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("%s:%d", host, port)
}
