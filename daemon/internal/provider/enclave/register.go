package enclave

import (
	"path/filepath"

	"github.com/eclipsesource/homeshell/daemon/internal/hostbridge"
	"github.com/eclipsesource/homeshell/daemon/internal/persist"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
)

func init() {
	provider.RegisterSessionFactory(func() (provider.SessionProvider, error) {
		base, err := persist.DefaultDir()
		if err != nil {
			return nil, err
		}
		runtime := hostbridge.New(filepath.Join(base, "enclave"))
		return NewWithBridge(NewCLI(), runtime, filepath.Join(base, "enclave-provider.json"))
	})
}
