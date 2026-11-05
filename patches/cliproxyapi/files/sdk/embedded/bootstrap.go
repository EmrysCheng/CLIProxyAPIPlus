// Package embedded supplies the CLI initialization missing from the public SDK entrypoint.
package embedded

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// Initialize is called once before constructing a service. Optional stores and Home
// bootstrap are intentionally not implemented by the P0 prototype.
func Initialize(localCatalogs bool) {
	logging.SetupBaseLogger()
	management.SetEmbeddedCallbacks(true)
	registry.SetLocalModelCatalogs(localCatalogs)
	sdkAuth.RegisterTokenStore(sdkAuth.NewFileTokenStore())
}

func InstallConfigPolicy(fn func(*config.Config) error) (func(), error) {
	return internalconfig.InstallEmbeddedPolicy(fn)
}

func PluginsSupported() bool { return pluginhost.SupportPluginHeaderValue() == "1" }
