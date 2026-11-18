// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { useState } from 'react'
import { Loader2 } from 'lucide-react'
import { useConfirm } from '@/components/ConfirmDialog'
import { Field } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { useT } from '@/i18n'
import { testGitSettings, useDisconnectGit, useSaveGitSettings, type GitCheck, type GitSettingsView } from '@/lib/api'

/** Connect a repository, or change the one in use: address, branch, folder, token, push mode. */
export default function GitSettingsForm({ view, onDone }: { view: GitSettingsView; onDone?: () => void }) {
  const { t, tn } = useT()
  const save = useSaveGitSettings()
  const disconnect = useDisconnectGit()
  const [confirmDialog, confirm] = useConfirm()
  const [url, setUrl] = useState(view.connected ? view.url : '')
  const [branch, setBranch] = useState(view.branch || 'main')
  const [path, setPath] = useState(view.path ?? '')
  const [write, setWrite] = useState<string>(view.write || 'off')
  const [token, setToken] = useState('')
  const [check, setCheck] = useState<GitCheck | null>(null)
  const [testing, setTesting] = useState(false)
  const body = () => ({ url: url.trim(), branch: branch.trim(), path: path.trim(), write: (write === 'off' ? '' : write) as '' | 'pullRequest' | 'commit', token: token.trim() || undefined })
  const test = () => {
    setTesting(true)
    setCheck(null)
    testGitSettings(body())
      .then(setCheck)
      .catch((e: unknown) => setCheck({ ok: false, files: 0, error: e instanceof Error ? e.message : String(e) }))
      .finally(() => setTesting(false))
  }
  return (
    <div className="space-y-4" data-testid="git-settings">
      <Field label={t('gits.url')} htmlFor="git-url">
        <Input id="git-url" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://github.com/your-team/observability.git" autoComplete="off" />
      </Field>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Branch" htmlFor="git-branch">
          <Input id="git-branch" value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="main" />
        </Field>
        <Field label={t('gits.path')} htmlFor="git-path" hint={t('gits.pathHint')}>
          <Input id="git-path" value={path} onChange={(e) => setPath(e.target.value)} placeholder="/" />
        </Field>
      </div>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Token" htmlFor="git-token" hint={view.hasToken ? t('gits.tokenKept') : t('gits.tokenHint')}>
          <Input id="git-token" type="password" value={token} onChange={(e) => setToken(e.target.value)} autoComplete="new-password" placeholder={view.hasToken ? '••••••••' : ''} />
        </Field>
        <Field label="Push">
          <Select value={write} onValueChange={setWrite}>
            <SelectTrigger data-testid="git-write">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="off">{t('gits.write.off')}</SelectItem>
              <SelectItem value="pullRequest">Pull request</SelectItem>
              <SelectItem value="commit">{t('gits.write.commit')}</SelectItem>
            </SelectContent>
          </Select>
        </Field>
      </div>
      {check && (
        <p className={check.ok ? 'text-sm text-ok' : 'text-sm text-destructive'} data-testid="git-check">
          {check.ok ? (check.empty ? t('gits.check.empty') : tn('gits.check.ok', check.files)) : check.error}
        </p>
      )}
      {save.isError && <p className="text-sm text-destructive">{save.error.message}</p>}
      <div className="flex flex-wrap items-center gap-2">
        <Button onClick={() => save.mutate(body(), { onSuccess: onDone })} disabled={!url.trim() || save.isPending} data-testid="git-connect">
          {save.isPending && <Loader2 className="animate-spin" />}
          {t(view.connected ? 'system.save' : 'gits.connect')}
        </Button>
        <Button variant="outline" onClick={test} disabled={!url.trim() || testing}>
          {testing && <Loader2 className="animate-spin" />}
          {t('gits.test')}
        </Button>
        {view.connected && (
          <Button
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
    </div>
  )
}
