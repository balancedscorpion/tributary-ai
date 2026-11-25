import { useEffect, useState } from 'react';

export function useMediaQuery(query: string) {
  const [matches, setMatches] = useState(() => {
    if (typeof window === 'undefined') return false;
    return window.matchMedia(query).matches;
  });

  useEffect(() => {
    const mql = window.matchMedia(query);

    const onChange = (event: MediaQueryListEvent) => {
      setMatches(event.matches);
    };

    // Keep state in sync even if query changes.
    setMatches(mql.matches);

    // Safari < 14 fallback
    if ('addEventListener' in mql) {
      mql.addEventListener('change', onChange);
      return () => mql.removeEventListener('change', onChange);
    }

    // @ts-expect-error - legacy Safari types
    mql.addListener(onChange);
    // @ts-expect-error - legacy Safari types
    return () => mql.removeListener(onChange);
  }, [query]);

  return matches;
}

