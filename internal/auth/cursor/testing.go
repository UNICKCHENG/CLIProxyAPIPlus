package cursor

import "fmt"

// BindBridgeForTesting installs one pre-connected bridge process into the runtime's
// pool for the no-proxy key. baseURL must already serve the sdk.v1 Connect services
// (transport tcp, protocol connect) behind the given bearer token. It exists so
// executor-level tests can drive RunChat and RunChatStream against an in-process fake
// bridge without spawning a real one; it must never be called outside tests.
func (r *Runtime) BindBridgeForTesting(baseURL, authToken, workspace string) error {
	if r == nil {
		return fmt.Errorf("cursor runtime is nil")
	}
	process := &bridgeProcess{
		runtime:   r,
		workspace: workspace,
		stderr:    &tailBuffer{},
		exited:    make(chan struct{}),
	}
	if errBind := process.bindClients(bridgeDiscovery{
		SchemaVersion: bridgeDiscoverySchema,
		Transport:     "tcp",
		Protocol:      "connect",
		URL:           baseURL,
		AuthToken:     authToken,
	}); errBind != nil {
		return errBind
	}
	if errCallback := startToolCallback(process); errCallback != nil {
		return errCallback
	}
	r.bridgeMu.Lock()
	previous := r.bridges
	r.bridges = map[string]*bridgeProcess{"": process}
	r.bridgeMu.Unlock()
	for _, old := range previous {
		old.stop()
	}
	return nil
}
