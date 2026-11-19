// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { useState, type FormEvent, type ReactNode } from 'react'
import { ChevronRight, FolderGit2, FolderOpen, FolderPlus, Loader2 } from 'lucide-react'
import { useNavigate } from 'react-router'
import { Field } from '@/components/common'
import RepoProblem from '@/components/RepoProblem'
import LangSwitch from '@/components/LangSwitch'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { useT } from '@/i18n'
import { checkProjectRepo, useCloneProject, useNewProject, useOpenProject, useSaveGitSettings, type GitCheck, type ProjectInfo } from '@/lib/api'
import { checkAction, joinPath, repoName, slug } from '@/lib/git'

function Card({ icon, title, children, testid }: { icon: ReactNode; title: string; children: ReactNode; testid: string }) {
  return (
    <section className="flex flex-col rounded-xl border bg-card p-5" data-testid={testid}>
      <h2 className="mb-4 flex items-center gap-2 text-base font-medium">
        {icon}
        {title}
      </h2>
      {children}
    </section>
  )
}

/**
 * The first screen of a new installation: no project yet, so nothing else to show. A project is
 * made here, or taken from git, the way an editor starts: a name and a place, or a clone. Run on
 * a machine, the place is a folder on its disk; run in a cluster, the place is the cluster.
 */
export default function ProjectSetup({ info, userBox }: { info: ProjectInfo; userBox?: ReactNode }) {
  const { t } = useT()
  const navigate = useNavigate()
  const done = (to = '/') => void navigate(to, { replace: true })
  const local = info.where === 'local'
  return (
    <div className="min-h-svh bg-muted/30 px-4 py-8 sm:py-14" data-testid="project-setup">
      <div className="mx-auto max-w-4xl">
        <header className="mb-6 flex items-center gap-2">
          <img src="/favicon.svg" alt="" className="size-6" />
          <span className="text-base font-semibold">Kamerton</span>
          <span className="ml-auto flex items-center gap-1">
            <LangSwitch />
            {userBox}
          </span>
        </header>
        <h1 className="text-xl font-semibold">{t('setup.title')}</h1>
        <p className="mt-1 mb-6 text-sm text-muted-foreground">{t(local ? 'setup.leadLocal' : 'setup.leadCluster')}</p>
        <div className="grid items-start gap-4 md:grid-cols-2">
          <NewProject info={info} onDone={done} />
          {info.canClone && <Clone info={info} onDone={done} />}
        </div>
        {local && <OpenLocal info={info} onDone={done} />}
      </div>
    </div>
  )
}

function NewProject({ info, onDone }: { info: ProjectInfo; onDone: (to?: string) => void }) {
  const { t } = useT()
  const make = useNewProject()
  const local = info.where === 'local'
  const [name, setName] = useState('')
  // The folder follows the name until it is typed in by hand.
  const [path, setPath] = useState<string | null>(null)
  const [git, setGit] = useState(false)
  const folder = path ?? (name.trim() ? joinPath(info.defaultRoot ?? '', slug(name)) : '')
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (make.isPending) return
    make.mutate(
      { name: name.trim(), path: local ? folder.trim() : undefined, git: local ? git : undefined },
      // In a cluster the repository is connected on the project page, right after.
      { onSuccess: () => onDone(!local && git ? '/project' : '/') },
    )
  }
  return (
    <Card icon={<FolderPlus className="size-5 text-muted-foreground" />} title={t('setup.new')} testid="setup-new">
      <form className="space-y-4" onSubmit={submit} noValidate>
        <Field label={t('setup.name')} htmlFor="project-name">
          <Input id="project-name" value={name} onChange={(e) => (setName(e.target.value), make.reset())} placeholder={t('setup.namePlaceholder')} autoFocus autoComplete="off" />
        </Field>
        {local ? (
          <Field label={t('setup.where')} htmlFor="project-path" hint={t('setup.whereLocal')}>
            <Input id="project-path" className="font-mono text-xs" value={folder} onChange={(e) => (setPath(e.target.value), make.reset())} placeholder={joinPath(info.defaultRoot ?? '~/kamerton', 'shop')} spellCheck={false} />
          </Field>
        ) : (
          <div className="space-y-1">
            <div className="text-sm font-medium">{t('setup.where')}</div>
            <p className="text-sm">{t('setup.whereCluster', { ns: info.namespace || 'kamerton' })}</p>
            <p className="text-xs text-muted-foreground">{t('setup.whereClusterHint')}</p>
          </div>
        )}
        {(local || info.canClone) && (
          <label className="flex items-start gap-2 text-sm">
            <Checkbox checked={git} onCheckedChange={(v) => setGit(v === true)} className="mt-0.5" />
            <span>
              {t(local ? 'setup.gitInit' : 'setup.gitNow')}
              <span className="block text-xs text-muted-foreground">{t(local ? 'setup.gitInitHint' : 'setup.gitNowHint')}</span>
            </span>
          </label>
        )}
        {make.isError && <p className="text-sm text-destructive">{make.error.message}</p>}
        <Button type="submit" className="w-full" disabled={!name.trim() || make.isPending} data-testid="setup-create">
          {make.isPending && <Loader2 className="animate-spin" />}
          {t('setup.create')}
        </Button>
      </form>
    </Card>
  )
}

type Write = 'pullRequest' | 'commit' | 'off'

/**
 * Take the project from a repository. The repository is read first, and the button then does
 * what fits: open the project that is there, or start one in it. On a machine the repository is
 * cloned into a folder; in a cluster it is followed from there.
 */
function Clone({ info, onDone }: { info: ProjectInfo; onDone: (to?: string) => void }) {
  const { t, tn } = useT()
  const local = info.where === 'local'
  const clone = useCloneProject()
  const connect = useSaveGitSettings()
  const busy = clone.isPending || connect.isPending
  const failure = clone.error ?? connect.error
  const [url, setUrl] = useState('')
  const [branch, setBranch] = useState('main')
  const [token, setToken] = useState('')
  const [folder, setFolder] = useState('')
  const [dest, setDest] = useState<string | null>(null)
  const [write, setWrite] = useState<Write>('pullRequest')
  const [name, setName] = useState<string | null>(null)
  const [more, setMore] = useState(false)
  const [check, setCheck] = useState<GitCheck | null>(null)
  const [testing, setTesting] = useState(false)

  const repo = repoName(url)
  const target = dest ?? (repo ? joinPath(info.defaultRoot ?? '', slug(repo)) : '')
  const projectName = name ?? repo
  const action = check?.ok ? checkAction(check) : null
  const creating = action === 'create'
  const mode: Write = creating && write === 'off' ? 'pullRequest' : write
  const missingToken = !!check?.ok && !check.canPush && (creating || (!local && mode !== 'off'))

  // A check is about the repository as it was typed: changing it makes the check stale.
  const edit =
    (set: (v: string) => void) =>
    (v: string) => {
      set(v)
      setCheck(null)
      clone.reset()
      connect.reset()
    }
  const body = { url: url.trim(), branch: branch.trim() || 'main', folder: folder.trim(), token: token.trim() || undefined }
  const test = () => {
    setTesting(true)
    setCheck(null)
    checkProjectRepo(body)
      .then(setCheck)
      .catch((e: unknown) => setCheck({ ok: false, files: 0, error: e instanceof Error ? e.message : String(e) }))
      .finally(() => setTesting(false))
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!url.trim() || testing || busy) return
    if (!check?.ok) return test()
    if (missingToken || (creating && !projectName.trim())) return
    const nameIfNew = creating ? projectName.trim() : undefined
    if (local) clone.mutate({ ...body, path: target.trim(), name: nameIfNew }, { onSuccess: () => onDone() })
    else
      connect.mutate(
        { url: body.url, branch: body.branch, path: body.folder, token: body.token, write: mode === 'off' ? '' : mode, create: creating || undefined, name: nameIfNew },
        { onSuccess: () => onDone() },
      )
  }
  const primary = !check?.ok
    ? t('gits.test')
    : creating
      ? t(local ? 'setup.cloneCreate' : 'gits.create')
      : local
        ? t('setup.cloneButton')
        : check.project
          ? t('gits.openProject', { project: check.project })
          : t('gits.open')
  const where = folder.trim() ? t('gits.inFolder', { path: folder.trim().replace(/^\/+|\/+$/g, '') }) : t('gits.inRoot')

  return (
    <Card icon={<FolderGit2 className="size-5 text-muted-foreground" />} title={t('setup.clone')} testid="setup-clone">
      <form className="space-y-4" onSubmit={submit} noValidate>
        <Field label={t('gits.url')} htmlFor="clone-url">
          <Input id="clone-url" value={url} onChange={(e) => edit(setUrl)(e.target.value)} placeholder="https://github.com/your-team/observability.git" autoComplete="off" spellCheck={false} />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Branch" htmlFor="clone-branch">
            <Input id="clone-branch" value={branch} onChange={(e) => edit(setBranch)(e.target.value)} placeholder="main" />
          </Field>
          <Field label="Token" htmlFor="clone-token" hint={t('gits.tokenHint')}>
            <Input id="clone-token" type="password" value={token} onChange={(e) => edit(setToken)(e.target.value)} autoComplete="new-password" />
          </Field>
        </div>
        {local && (
          <Field label={t('setup.cloneTo')} htmlFor="clone-dest" hint={t('setup.whereLocal')}>
            <Input id="clone-dest" className="font-mono text-xs" value={target} onChange={(e) => setDest(e.target.value)} placeholder={joinPath(info.defaultRoot ?? '~/kamerton', 'observability')} spellCheck={false} />
          </Field>
        )}
        <div>
          <button type="button" className="flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground" onClick={() => setMore((v) => !v)} aria-expanded={more} data-testid="clone-more">
            <ChevronRight className={more ? 'size-4 rotate-90 transition-transform' : 'size-4 transition-transform'} />
            {t('setup.more')}
          </button>
          {more && (
            <div className="mt-3 grid gap-4 sm:grid-cols-2">
              <Field label={t('gits.path')} htmlFor="clone-folder" hint={t('gits.pathHint')}>
                <Input id="clone-folder" value={folder} onChange={(e) => edit(setFolder)(e.target.value)} placeholder={t('gits.root')} />
              </Field>
              {!local && (
                <Field label="Push">
                  <Select value={mode} onValueChange={(v) => setWrite(v as Write)}>
                    <SelectTrigger data-testid="clone-write">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {!creating && <SelectItem value="off">{t('gits.write.off')}</SelectItem>}
                      <SelectItem value="pullRequest">Pull request</SelectItem>
                      <SelectItem value="commit">{t('gits.write.commit')}</SelectItem>
                    </SelectContent>
                  </Select>
                </Field>
              )}
            </div>
          )}
        </div>
        {check && (
          <div className="space-y-1 text-sm" data-testid="clone-check">
            {!check.ok ? (
              <RepoProblem check={check} />
            ) : (
              <p>
                {check.empty
                  ? t('gits.check.empty')
                  : check.hasProject
                    ? t('gits.check.project', { project: check.project ?? '' }) + ' ' + tn('gits.check.files', check.files)
                    : check.files > 0
                      ? tn('gits.check.ok', check.files) + ' ' + t('gits.check.noProject')
                      : t('gits.check.nothing')}
                {creating && ' ' + t('gits.check.willCreate', { where })}
              </p>
            )}
            {missingToken && <p className="text-destructive">{t(creating ? 'gits.needToken.create' : 'gits.needToken.push')}</p>}
          </div>
        )}
        {creating && (
          <Field label={t('setup.name')} htmlFor="clone-name">
            <Input id="clone-name" value={projectName} onChange={(e) => setName(e.target.value)} autoComplete="off" />
          </Field>
        )}
        {failure && <p className="text-sm text-destructive">{failure.message}</p>}
        <Button
          type="submit"
          variant={check?.ok ? 'default' : 'outline'}
          className="w-full"
          disabled={!url.trim() || testing || busy || (!!check?.ok && (missingToken || (creating && !projectName.trim())))}
          data-testid="setup-clone-submit"
        >
          {(testing || busy) && <Loader2 className="animate-spin" />}
          {primary}
        </Button>
      </form>
    </Card>
  )
}

/** A project that is already on this machine: one opened before, or any folder with a project. */
function OpenLocal({ info, onDone }: { info: ProjectInfo; onDone: () => void }) {
  const { t } = useT()
  const open = useOpenProject()
  const [path, setPath] = useState('')
  const go = (p: string) => open.mutate(p, { onSuccess: () => onDone() })
  return (
    <section className="mt-4 rounded-xl border bg-card p-5" data-testid="setup-open">
      <h2 className="mb-3 flex items-center gap-2 text-base font-medium">
        <FolderOpen className="size-5 text-muted-foreground" />
        {t('setup.open')}
      </h2>
      {info.recent && info.recent.length > 0 && (
        <ul className="mb-4 divide-y" data-testid="setup-recent">
          {info.recent.map((r) => (
            <li key={r.path}>
              <button type="button" className="flex w-full flex-wrap items-baseline gap-x-3 py-2 text-left text-sm hover:underline" onClick={() => go(r.path)} disabled={open.isPending}>
                <span className="font-medium">{r.name}</span>
                <span className="min-w-0 font-mono text-xs text-muted-foreground [overflow-wrap:anywhere]">{r.path}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
      <form
        className="flex flex-wrap gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          if (path.trim() && !open.isPending) go(path.trim())
        }}
      >
        <Input className="min-w-0 flex-1 font-mono text-xs" value={path} onChange={(e) => (setPath(e.target.value), open.reset())} placeholder={joinPath(info.defaultRoot ?? '~/kamerton', 'shop')} aria-label={t('setup.openPath')} spellCheck={false} />
        <Button type="submit" variant="outline" disabled={!path.trim() || open.isPending}>
          {open.isPending && <Loader2 className="animate-spin" />}
          {t('setup.openButton')}
        </Button>
      </form>
      {open.isError && <p className="mt-2 text-sm text-destructive">{open.error.message}</p>}
    </section>
  )
}
