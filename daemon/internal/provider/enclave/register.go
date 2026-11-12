package enclave

import (
	"os"
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
		enclaveProvider, err := NewWithBridge(NewCLI(), runtime, filepath.Join(base, "enclave-provider.json"))
		if err != nil {
			return nil, err
		}
		enclaveProvider.inventoryEvery = inventoryIntervalFrom(os.Getenv(inventoryIntervalEnv))
		return enclaveProvider, nil
	})
}
