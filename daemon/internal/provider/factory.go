package provider

// SessionFactory constructs a session provider at daemon startup, or returns
// (nil, nil) to decline (e.g. when its backend is fundamentally absent).
// Availability that can change at runtime belongs in Available, not here.
type SessionFactory func() (SessionProvider, error)

// sessionFactories holds the self-registered session-provider factories.
// Provider packages append to it from an init(), so a provider joins the daemon
// by being imported — serve wires no provider by name. This mirrors the
// self-registration idiom used by the cli verbs and api endpoints.
var sessionFactories []SessionFactory

// RegisterSessionFactory adds a session-provider factory. Call it from a
// provider package's init(); the daemon materializes the factories into its
// registry at startup via RegisterSessionFactories.
func RegisterSessionFactory(f SessionFactory) { sessionFactories = append(sessionFactories, f) }

// RegisterSessionFactories instantiates every self-registered session factory
// into reg, in registration order, skipping the ones that decline. The daemon
// calls this once at startup after creating its registry.
func RegisterSessionFactories(reg *Registry) error {
	for _, f := range sessionFactories {
		p, err := f()
		if err != nil {
			return err
		}
		if p == nil {
			continue
		}
		if err := reg.RegisterSessionProvider(p); err != nil {
			return err
		}
	}
	return nil
}
