import { QueryClientProvider } from '@tanstack/react-query';
import { useEffect } from 'react';
import { createBrowserRouter, RouterProvider } from 'react-router';
import { useProfilePreferences } from '@/auth/profile';
import { bootstrapSession } from '@/auth/session';
import { Toaster } from '@/components/ui/sonner';
import { TooltipProvider } from '@/components/ui/tooltip';
import { queryClient } from './queryClient';
import { routes } from './routes';

const router = createBrowserRouter(routes);

function SessionEffects() {
  useProfilePreferences();
  useEffect(() => {
    void bootstrapSession();
  }, []);
  return null;
}

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <TooltipProvider delayDuration={300}>
        <SessionEffects />
        <RouterProvider router={router} />
        <Toaster position="top-right" richColors closeButton />
      </TooltipProvider>
    </QueryClientProvider>
  );
}
