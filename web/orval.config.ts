import { defineConfig } from 'orval';

// Generates typed TanStack Query hooks from the OpenAPI spec (the API source of truth).
// Run `npm run api:generate` after changing api/openapi.yaml. Output is committed.
export default defineConfig({
  opshub: {
    // The runner API is for the opshub-runner agent (Go), not the browser.
    input: { target: '../api/openapi.yaml', filters: { mode: 'exclude', tags: ['runner-api'] } },
    output: {
      mode: 'tags-split',
      target: 'src/lib/api/generated',
      schemas: 'src/lib/api/generated/model',
      client: 'react-query',
      httpClient: 'fetch',
      clean: true,
      override: {
        mutator: { path: 'src/lib/api/fetcher.ts', name: 'customFetch' },
        fetch: { includeHttpResponseReturnType: false },
        query: { useQuery: true, useSuspenseQuery: false },
      },
    },
  },
});
