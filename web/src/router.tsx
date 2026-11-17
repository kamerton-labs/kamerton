// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import type { ComponentType } from 'react'
import { createBrowserRouter, Navigate } from 'react-router'
import AppShell from '@/components/layout/AppShell'
import ErrorPage from '@/pages/ErrorPage'
import NotFoundPage from '@/pages/NotFoundPage'
import SystemsPage from '@/pages/SystemsPage'

// Heavy screens (React Flow, CodeMirror) are code-split.
const lazyPage = (load: () => Promise<{ default: ComponentType }>) => async () => ({ Component: (await load()).default })
const runbook = lazyPage(() => import('@/pages/RunbookPage'))

export const router = createBrowserRouter([
  // Outside the shell: shown only when login is enabled on the core.
  { path: '/login', lazy: lazyPage(() => import('@/pages/LoginPage')), errorElement: <ErrorPage /> },
  {
    path: '/',
    element: <AppShell />,
    errorElement: <ErrorPage />,
    children: [
      { index: true, element: <SystemsPage /> },
      { path: 'systems/:id', lazy: lazyPage(() => import('@/pages/SystemPage')) },
      { path: 'systems/:id/runbooks/:service/:alert?', lazy: runbook },
      { path: 'alerts', lazy: lazyPage(() => import('@/pages/AlertsPage')) },
      { path: 'channels', lazy: lazyPage(() => import('@/pages/ChannelsPage')) },
      { path: 'status', lazy: lazyPage(() => import('@/pages/StatusPage')) },
      // Links in notifications that were already sent carry no system.
      { path: 'runbooks/:service/:alert?', lazy: runbook },
      // Screens of earlier versions.
      { path: 'setup/channels', element: <Navigate to="/channels" replace /> },
      { path: 'setup/stack', element: <Navigate to="/status" replace /> },
      { path: 'setup/*', element: <Navigate to="/" replace /> },
      { path: 'setup', element: <Navigate to="/" replace /> },
      { path: 'description', element: <Navigate to="/" replace /> },
      { path: 'map', element: <Navigate to="/" replace /> },
      { path: '*', element: <NotFoundPage /> },
    ],
  },
])
