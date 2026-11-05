package webupdate

import (
	manager "github.com/seakee/cpa-manager-plus/apps/manager-server/pkg/embedded"
	"io"
)

func acquireOperationLock(o Options) (io.Closer, error) { return manager.AcquireDataLock(o.stateDir()) }
