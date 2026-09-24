import { useEffect, useState } from 'react';
import { useLocation, useNavigate } from 'react-router';

/**
 * Reads a one-time token from the URL fragment ("#token=…") once, then removes the fragment
 * through the router. Fragments are never sent to servers, so the token stays out of logs and
 * Referer headers, and removing it keeps it out of browser history and screenshots.
 */
export function useHashToken(): string | null {
  const location = useLocation();
  const navigate = useNavigate();
  const [token] = useState(() => new URLSearchParams(location.hash.slice(1)).get('token'));
  useEffect(() => {
    if (location.hash) void navigate({ pathname: location.pathname, search: location.search }, { replace: true });
  }, [location.hash, location.pathname, location.search, navigate]);
  return token;
}
