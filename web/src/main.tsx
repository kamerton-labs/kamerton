// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from 'react-router/dom'
import { ApiError } from '@/lib/api'
import { isLoginPath, loginPath, setUnauthorizedHandler } from '@/lib/auth'
import { ThemeProvider } from '@/lib/theme'
import { router } from '@/router'
import './index.css'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // A missing session is not fixed by retrying.
      retry: (count, err) => !(err instanceof ApiError && err.status === 401) && count < 1,
      refetchOnWindowFocus: false,
    },
  },
})

// Any 401: go to the login page and come back here afterwards.
setUnauthorizedHandler(() => {
  const { pathname, search, hash } = router.state.location
  if (isLoginPath(pathname)) return
  queryClient.setQueryData(['session'], { enabled: true, authenticated: false, user: '' })
  void router.navigate(loginPath(pathname + search + hash), { replace: true })
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ThemeProvider>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </ThemeProvider>
  </StrictMode>,
)
