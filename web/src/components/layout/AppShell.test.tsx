// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { setLang } from '@/i18n'
import type { GitCheck, ProjectInfo } from '@/lib/api'
import { checkAction, joinPath, repoName, slug } from '@/lib/git'

const state: { project?: ProjectInfo; loading: boolean } = { loading: false }
const q = (data: unknown) => ({ isLoading: state.loading, isError: false, data: state.loading ? undefined : data })
const m = { mutate: () => {}, reset: () => {}, isPending: false, isError: false, error: null }

vi.mock('@/lib/api', () => ({
  useSession: () => ({ data: { enabled: false, authenticated: true, user: '' } }),
  useProject: () => q(state.project),
  useSystems: () => q({ systems: [] }),
  useVersion: () => q({ version: '0.3.0' }),
  useAlerts: () => q({ alerts: [] }),
  useStacks: () => q({ sets: [] }),
  useNewProject: () => m,
  useOpenProject: () => m,
  useCloneProject: () => m,
  useSaveGitSettings: () => m,
  checkProjectRepo: () => Promise.resolve({ ok: true, files: 0 }),
}))

vi.mock('@/lib/theme', () => ({ useTheme: () => ({ theme: 'light', toggle: () => {} }) }))

const { default: AppShell } = await import('./AppShell')
const render = () => renderToStaticMarkup(<MemoryRouter><AppShell /></MemoryRouter>)
const local: ProjectInfo = { exists: false, where: 'local', defaultRoot: '/home/anna/kamerton', canClone: true, recent: [{ name: 'shop', path: '/home/anna/kamerton/shop' }] }
const cluster: ProjectInfo = { exists: false, where: 'cluster', namespace: 'monitoring', canClone: true }

describe('Kamerton starts with its project', () => {
  beforeEach(() => {
    setLang('en')
    Object.assign(state, { project: local, loading: false })
    vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener: () => {}, removeEventListener: () => {} }))
  })

  it('shows only the first screen while no project is open', () => {
    const html = render()
    expect(html).toContain('data-testid="project-setup"')
    expect(html).not.toContain('<aside')
  })

  it('on a machine: a name and a folder, a clone, and the projects opened before', () => {
    const html = render()
    expect(html).toContain('data-testid="setup-new"')
    expect(html).toContain('id="project-path"')
    expect(html).toContain('data-testid="setup-clone"')
    expect(html).toContain('id="clone-dest"')
    expect(html).toContain('/home/anna/kamerton/shop')
  })

  it('in a cluster: no folder anywhere, the namespace instead', () => {
    state.project = cluster
    const html = render()
    expect(html).toContain('In the cluster, namespace monitoring')
    expect(html).not.toContain('id="project-path"')
    expect(html).not.toContain('id="clone-dest"')
    expect(html).not.toContain('data-testid="setup-open"')
  })

  it('offers no clone where a repository cannot be connected', () => {
    state.project = { ...cluster, canClone: false }
    expect(render()).not.toContain('data-testid="setup-clone"')
  })

  it('shows nothing while it is not known yet, so the sidebar does not flash', () => {
    state.loading = true
    const html = render()
    expect(html).not.toContain('<aside')
    expect(html).not.toContain('project-setup')
  })

  it('opens the app once a project exists', () => {
    state.project = { ...local, exists: true, name: 'shop', path: '/home/anna/kamerton/shop' }
    const html = render()
    expect(html).toContain('<aside')
    expect(html).toContain('data-testid="project-box"')
  })
})

describe('helpers of the first screen', () => {
  const c = (over: Partial<GitCheck>): GitCheck => ({ ok: true, files: 0, ...over })
  it('opens a project or a folder of descriptions, creates everything else', () => {
    expect(checkAction(c({ hasProject: true, project: 'obs' }))).toBe('open')
    expect(checkAction(c({ files: 3 }))).toBe('open')
    expect(checkAction(c({ empty: true }))).toBe('create')
    expect(checkAction(c({}))).toBe('create')
  })
  it('makes folder names like the core does', () => {
    expect(slug('Мой магазин')).toBe('мой-магазин')
    expect(slug('!!!')).toBe('project')
    expect(repoName('https://github.com/team/obs.git')).toBe('obs')
    expect(repoName('git@github.com:team/obs.git')).toBe('obs')
    expect(joinPath('/home/anna/kamerton/', 'shop')).toBe('/home/anna/kamerton/shop')
    expect(joinPath('C:\\Users\\anna\\kamerton', 'shop')).toBe('C:\\Users\\anna\\kamerton\\shop')
  })
})
