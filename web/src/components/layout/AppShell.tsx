// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { useSyncExternalStore } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Activity, BellRing, Boxes, FolderGit2, GitBranch, LogOut, Moon, Send, Sun, UserRound } from 'lucide-react'
import { Navigate, NavLink, Outlet, useLocation, useNavigate } from 'react-router'
import { cn } from 'cn'
import { StateDot } from '@/components/common'
import LangSwitch from '@/components/LangSwitch'
import { shortVersion } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { useAlerts, useSession, useStacks, useSystems, useVersion } from '@/lib/api'
import { gitProblems, projectName } from '@/lib/git'
import { loginPath, logout, LOGIN_PATH } from '@/lib/auth'
import { coreHealth } from '@/lib/stacks'
import { useT } from '@/i18n'
import { useTheme } from '@/lib/theme'

const nav = [
  { to: '/', label: 'nav.systems', icon: Boxes, end: false, match: (p: string) => p === '/' || p.startsWith('/systems') },
  { to: '/alerts', label: 'nav.alerts', icon: BellRing, end: true, match: (p: string) => p.startsWith('/alerts') || p.includes('/runbooks/') },
  { to: '/channels', label: 'nav.channels', icon: Send, end: true, match: (p: string) => p.startsWith('/channels') },
  { to: '/project', label: 'nav.git', icon: FolderGit2, end: true, match: (p: string) => p.startsWith('/project') },
] as const

function ThemeToggle() {
  const { theme, toggle } = useTheme()
  const { t } = useT()
  const Icon = theme === 'dark' ? Moon : Sun
  return (
    <Button variant="ghost" size="icon-sm" aria-label={t('shell.theme')} title={theme === 'dark' ? t('shell.themeDark') : t('shell.themeLight')} onClick={toggle}>
      <Icon />
    </Button>
  )
}

const desktopQuery = '(min-width: 48rem)'
const subscribeDesktop = (cb: () => void) => {
  const mq = window.matchMedia(desktopQuery)
  mq.addEventListener('change', cb)
  return () => mq.removeEventListener('change', cb)
}
/** Matches Tailwind's md breakpoint, so that the user box is rendered once (sidebar or mobile header). */
const useIsDesktop = () => useSyncExternalStore(subscribeDesktop, () => window.matchMedia(desktopQuery).matches)

/** Signed-in user and logout; shown only when login is enabled. */
function UserBox({ user, compact }: { user: string; compact?: boolean }) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const { t } = useT()
  const onLogout = async () => {
    try {
      await logout()
    } finally {
      qc.clear()
      void navigate(LOGIN_PATH, { replace: true })
    }
  }
  return (
    <div className={cn('flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground', !compact && 'px-2')} title={user}>
      <UserRound className="size-3.5 shrink-0" />
      {!compact && (
        <span className="min-w-0 flex-1 truncate" data-testid="current-user">
          {user}
        </span>
      )}
      <Button variant="ghost" size={compact ? 'icon-sm' : 'xs'} data-testid="logout-button" aria-label={t('shell.logout')} onClick={() => void onLogout()}>
        <LogOut />
        {!compact && t('shell.logout')}
      </Button>
    </div>
  )
}

function AlertCount() {
  const alerts = useAlerts()
  const n = alerts.data?.alerts.length ?? 0
  if (!n) return null
  const critical = alerts.data!.alerts.some((a) => a.severity === 'critical')
  return <span className={cn('ml-auto rounded-full px-1.5 text-xs font-medium tabular-nums', critical ? 'bg-firing/15 text-firing' : 'bg-warn/15 text-warn')}>{n}</span>
}

/** Open requests waiting for a merge, or a mark when the repository needs attention. */
function GitCount() {
  const data = useSystems().data
  const git = data?.git
  if (!git) return null
  if (git.error || gitProblems(git) > 0) return <span className="ml-auto rounded-full bg-firing/15 px-1.5 text-xs font-medium text-firing">!</span>
  const unpushed = data?.gitChanges?.length ?? 0
  if (unpushed > 0)
    return (
      <span className="ml-auto rounded-full bg-warn/15 px-1.5 text-xs font-medium tabular-nums text-warn" data-testid="git-unpushed-count" title="Not pushed">
        {unpushed}
      </span>
    )
  const n = git.requests?.length ?? 0
  if (!n) return null
  return (
    <span className="ml-auto rounded-full bg-muted px-1.5 text-xs font-medium tabular-nums text-muted-foreground" data-testid="git-count">
      {n}
    </span>
  )
}

/** The project at the top of the sidebar: its name, and the branch and folder it is read from. */
function ProjectBox() {
  const git = useSystems().data?.git
  const { t } = useT()
  if (!git) return null
  const bad = !!git.error || gitProblems(git) > 0
  return (
    <NavLink
      to="/project"
      data-testid="project-box"
      title={git.repo}
      className={({ isActive }) => cn('mb-3 block rounded-md border px-2 py-1.5 hover:bg-sidebar-accent', isActive && 'bg-sidebar-accent')}
    >
      <div className="flex items-center gap-1.5">
        <FolderGit2 className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="truncate text-sm font-medium">{projectName(git)}</span>
        <StateDot state={bad ? 'firing' : git.commit ? 'ok' : 'unknown'} className="ml-auto" />
      </div>
      <div className="mt-0.5 flex items-center gap-1 truncate pl-5 text-xs text-muted-foreground">
        <GitBranch className="size-3 shrink-0" />
        <span className="truncate">
          {git.branch}
          {git.systemsPath ? ` · ${git.systemsPath}` : ''}
        </span>
      </div>
      {git.empty && <div className="mt-0.5 pl-5 text-xs text-muted-foreground">{t('shell.projectEmpty')}</div>}
    </NavLink>
  )
}

/** Link to the health of Prometheus and Alertmanager of every set in use. */
function StackLink({ compact }: { compact?: boolean }) {
  const stacks = useStacks()
  const { t } = useT()
  const health = coreHealth(stacks.data?.sets)
  const state = health.state === 'unknown' ? 'unknown' : health.state === 'ok' ? 'ok' : 'pending'
  return (
    <NavLink
      to="/status"
      data-testid="stack-link"
      className={({ isActive }) => cn('flex items-center gap-2 rounded-md px-2 py-1.5 text-xs text-muted-foreground hover:bg-sidebar-accent hover:text-foreground', isActive && 'bg-sidebar-accent text-foreground')}
      title={health.problem ? `${t('shell.status')}: ${health.problem}` : t('shell.status')}
    >
      <Activity className="size-3.5" />
      {!compact && <span>{t('shell.status')}</span>}
      <StateDot state={state} className="ml-auto" />
    </NavLink>
  )
}

export default function AppShell() {
  const version = useVersion()
  const session = useSession()
  const isDesktop = useIsDesktop()
  const { t } = useT()
  const { pathname, search, hash } = useLocation()
  // Login enabled and no session: go to the login page and come back here afterwards.
  if (session.data?.enabled && !session.data.authenticated) return <Navigate to={loginPath(pathname + search + hash)} replace />
  const user = session.data?.enabled ? session.data.user : ''
  return (
    <div className="flex min-h-svh">
      <aside className="sticky top-0 hidden h-svh w-52 shrink-0 flex-col border-r bg-sidebar p-3 text-sidebar-foreground md:flex">
        <div className="mb-4 flex items-center gap-2 px-2 pt-1">
          <img src="/favicon.svg" alt="" className="size-6" />
          <span className="text-base font-semibold">Kamerton</span>
        </div>
        <ProjectBox />
        <nav className="flex flex-col gap-0.5">
          {nav.map(({ to, label, icon: Icon, match }) => (
            <NavLink
              key={to}
              to={to}
              className={cn(
                'flex items-center gap-2 rounded-md px-2 py-1.5 text-sm hover:bg-sidebar-accent',
                match(pathname) && 'bg-sidebar-accent font-medium text-sidebar-accent-foreground',
              )}
            >
              <Icon className="size-4" />
              {t(label)}
              {to === '/alerts' && <AlertCount />}
              {to === '/project' && <GitCount />}
            </NavLink>
          ))}
        </nav>
        <div className="mt-auto space-y-1">
          <StackLink />
          <div className="flex items-center justify-between px-2 text-xs text-muted-foreground">
            <span className="truncate whitespace-nowrap" title={version.data ? t('shell.version', { v: version.data.version }) : undefined}>
              {version.data ? shortVersion(version.data.version) : version.isError ? t('shell.coreDown') : ''}
            </span>
            <div className="flex items-center gap-1">
              <LangSwitch />
              <ThemeToggle />
            </div>
          </div>
          {user && isDesktop && (
            <div className="mt-2 border-t pt-2">
              <UserBox user={user} />
            </div>
          )}
        </div>
      </aside>
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-30 flex h-14 items-center gap-1 border-b bg-background px-2 md:hidden">
          <img src="/favicon.svg" alt="" className="mx-1 size-6" />
          <nav className="flex min-w-0 flex-1 gap-0.5">
            {nav.map(({ to, label, icon: Icon, match }) => (
              <NavLink
                key={to}
                to={to}
                aria-label={t(label)}
                title={t(label)}
                className={cn('flex shrink-0 items-center gap-1.5 rounded-md px-2 py-1.5 text-sm', match(pathname) ? 'bg-muted font-medium' : 'text-muted-foreground')}
              >
                <Icon className="size-4" />
                <span className={match(pathname) ? '' : 'hidden min-[480px]:inline'}>{t(label)}</span>
              </NavLink>
            ))}
          </nav>
          <StackLink compact />
          <LangSwitch />
          <ThemeToggle />
          {user && !isDesktop && <UserBox user={user} compact />}
        </header>
        <main className="min-w-0 flex-1">
          <Outlet />
        </main>
      </div>
    </div>
  )
}
