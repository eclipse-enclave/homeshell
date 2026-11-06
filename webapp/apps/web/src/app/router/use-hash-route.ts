import { useCallback, useEffect, useState } from 'react';

/** The top-level views the app can route to. */
export type Route = 'home' | 'settings' | 'logs';

/** A parsed location: the view plus, for settings, the selected category. */
export interface RouteLocation {
  route: Route;
  /** The settings category (e.g. "appearance"), when on the settings route. */
  category?: string;
}

/** Parses a `location.hash` value into a route (and settings category). */
export function parseLocation(hash: string): RouteLocation {
  // Strip the leading '#' and the hashbang '!' if present, then split.
  const segments = hash
    .replace(/^#!?/, '')
    .split('/')
    .filter(Boolean);
  if (segments[0] === 'settings') {
    return { route: 'settings', category: segments[1] };
  }
  if (segments[0] === 'logs') {
    return { route: 'logs' };
  }
  return { route: 'home' };
}

function toHash(route: Route, category?: string): string {
  if (route === 'logs') return '#!/logs';
  if (route !== 'settings') return '#!/';
  return category ? `#!/settings/${category}` : '#!/settings';
}

/**
 * Minimal hashbang router. Reads the current location from `location.hash`,
 * re-renders on `hashchange`, and exposes `navigate` to change it. URLs look
 * like `#!/` (home), `#!/settings`, `#!/settings/<category>`, and `#!/logs`.
 */
export function useHashRoute() {
  const [location, setLocation] = useState<RouteLocation>(() =>
    parseLocation(window.location.hash)
  );

  useEffect(() => {
    const onHashChange = () => setLocation(parseLocation(window.location.hash));
    window.addEventListener('hashchange', onHashChange);
    return () => window.removeEventListener('hashchange', onHashChange);
  }, []);

  const navigate = useCallback((route: Route, category?: string) => {
    window.location.hash = toHash(route, category);
    // Update optimistically; `hashchange` fires asynchronously.
    setLocation({ route, category: route === 'settings' ? category : undefined });
  }, []);

  return {
    route: location.route,
    category: location.category,
    navigate,
  } as const;
}
