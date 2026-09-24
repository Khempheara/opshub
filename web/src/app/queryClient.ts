import { QueryClient } from '@tanstack/react-query';
import { ApiError } from '@/lib/api/fetcher';

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      // Client errors (4xx) won't succeed on retry.
      retry: (count, err) => !(err instanceof ApiError && err.status < 500) && count < 2,
    },
  },
});
