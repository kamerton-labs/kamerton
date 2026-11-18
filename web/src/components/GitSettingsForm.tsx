// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { useState, type FormEvent } from 'react'
import { Loader2 } from 'lucide-react'
import { useConfirm } from '@/components/ConfirmDialog'
import { Field } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { useT } from '@/i18n'
import { checkAction } from '@/lib/git'
import { testGitSettings, useDisconnectGit, useSaveGitSettings, type GitCheck, type GitSettingsView } from '@/lib/api'

type Write = 'off' | 'pullRequest' | 'commit'

/**
 * Connect a repository, or change the one in use. Before anything is connected the form first
 * reads the repository and then offers the one thing that fits: open the project there, or
 * create it. Nobody has to know in advance which of the two it is.
 */
export default function GitSettingsForm({ view, onDone }: { view: GitSettingsView; onDone?: () => void }) {
  const { t, tn } = useT()
  const save = useSaveGitSettings()
  const disconnect = useDisconnectGit()
  const [confirmDialog, confirm] = useConfirm()
  const [url, setUrl] = useState(view.connected ? view.url : '')
  const [branch, setBranch] = useState(view.branch || 'main')
  const [path, setPath] = useState(view.path ?? '')
  const [write, setWrite] = useState<Write>(view.connected ? ((view.write || 'off') as Write) : 'pullRequest')
  const [token, setToken] = useState('')
  const [check, setCheck] = useState<GitCheck | null>(null)
  const [testing, setTesting] = useState(false)

  // A check is about the fields as they were: any change makes it stale.
  const edit =
    <T,>(set: (v: T) => void) =>
    (v: T) => {
      set(v)
      setCheck(null)
      save.reset()
    }

  const action = check?.ok ? checkAction(check) : null
  const creating = !view.connected && action === 'create'
  // A new project is made by a push, so read only is not a choice for it.
  const mode: Write = creating && write === 'off' ? 'pullRequest' : write
  // The check was made with the token typed here or the stored one, so it knows.
  const missingToken = !!check?.ok && !check.canPush && (creating || mode !== 'off')

  const body = () => ({
    url: url.trim(),
    branch: branch.trim(),
    path: path.trim(),
    write: (mode === 'off' ? '' : mode) as '' | 'pullRequest' | 'commit',
    token: token.trim() || undefined,
    create: creating || undefined,
  })
  const test = () => {
    setTesting(true)
    setCheck(null)
    testGitSettings(body())
      .then(setCheck)
      .catch((e: unknown) => setCheck({ ok: false, files: 0, error: e instanceof Error ? e.message : String(e) }))
      .finally(() => setTesting(false))
  }
  // Not connected yet: check first, then the button does what the check found.
  const mustCheck = !view.connected && !check?.ok
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!url.trim() || testing || save.isPending) return
    if (mustCheck) return test()
    if (missingToken) return
    save.mutate(body(), { onSuccess: onDone })
  }
  const primary = mustCheck
    ? t('gits.test')
    : view.connected
      ? t('system.save')
      : creating
        ? t('gits.create')
        : check?.project
          ? t('gits.openProject', { project: check.project })
          : t('gits.open')
  const folder = path.trim().replace(/^\/+|\/+$/g, '')
  const where = folder ? t('gits.inFolder', { path: folder }) : t('gits.inRoot')

  return (
    <form className="space-y-4" data-testid="git-settings" onSubmit={submit} noValidate>
      <Field label={t('gits.url')} htmlFor="git-url">
        <Input
          id="git-url"
          value={url}
          onChange={(e) => edit(setUrl)(e.target.value)}
          placeholder="https://github.com/your-team/observability.git"
          autoComplete="off"
          autoFocus={!view.connected}
        />
      </Field>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Branch" htmlFor="git-branch">
          <Input id="git-branch" value={branch} onChange={(e) => edit(setBranch)(e.target.value)} placeholder="main" />
        </Field>
        <Field label={t('gits.path')} htmlFor="git-path" hint={t('gits.pathHint')}>
          <Input id="git-path" value={path} onChange={(e) => edit(setPath)(e.target.value)} placeholder={t('gits.root')} />
        </Field>
      </div>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Token" htmlFor="git-token" hint={view.hasToken ? t('gits.tokenKept') : t('gits.tokenHint')}>
          <Input
            id="git-token"
            type="password"
            value={token}
            onChange={(e) => edit(setToken)(e.target.value)}
            autoComplete="new-password"
            placeholder={view.hasToken ? '••••••••' : ''}
          />
        </Field>
        <Field label="Push">
          <Select value={mode} onValueChange={(v) => setWrite(v as Write)}>
            <SelectTrigger data-testid="git-write">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {!creating && <SelectItem value="off">{t('gits.write.off')}</SelectItem>}
              <SelectItem value="pullRequest">Pull request</SelectItem>
              <SelectItem value="commit">{t('gits.write.commit')}</SelectItem>
            </SelectContent>
          </Select>
        </Field>
      </div>
      {check && (
        <div className="space-y-1 text-sm" data-testid="git-check">
          {!check.ok ? (
            <p className="text-destructive">{check.error}</p>
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
      {save.isError && <p className="text-sm text-destructive">{save.error.message}</p>}
      <div className="flex flex-wrap items-center gap-2">
        <Button type="submit" disabled={!url.trim() || testing || save.isPending || (!mustCheck && missingToken)} data-testid="git-connect">
          {(save.isPending || (mustCheck && testing)) && <Loader2 className="animate-spin" />}
          {primary}
        </Button>
        {!mustCheck && (
          <Button type="button" variant="outline" onClick={test} disabled={!url.trim() || testing}>
            {testing && <Loader2 className="animate-spin" />}
            {t('gits.test')}
          </Button>
        )}
        {view.connected && (
          <Button
            type="button"
            variant="ghost"
            className="ml-auto text-destructive"
            disabled={disconnect.isPending}
            onClick={async () => {
              if (await confirm({ title: t('gits.disconnect.title'), description: t('gits.disconnect.text') })) disconnect.mutate(undefined, { onSuccess: onDone })
            }}
          >
            {t('gits.disconnect')}
          </Button>
        )}
      </div>
      {confirmDialog}
    </form>
  )
}
