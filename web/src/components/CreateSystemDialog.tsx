// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { useState } from 'react'
import { useNavigate } from 'react-router'
import { stringify } from 'yaml'
import { Field } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { useT } from '@/i18n'
import { ApiError, useCreateSystem, useDiscovery, useSystems, type GitProposal } from '@/lib/api'
import { NAME_RE } from '@/lib/model/types'

export default function CreateSystemDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (v: boolean) => void }) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90svh] overflow-y-auto sm:max-w-lg">{open && <Body onDone={() => onOpenChange(false)} />}</DialogContent>
    </Dialog>
  )
}

function Body({ onDone }: { onDone: () => void }) {
  const navigate = useNavigate()
  const create = useCreateSystem()
  const disc = useDiscovery()
  const systems = useSystems()
  const { t } = useT()
  const [name, setName] = useState('')
  const [about, setAbout] = useState('')
  const [picked, setPicked] = useState<string[]>([])
  // With writing to git on, a new system becomes a new file in the repository.
  const gitWrite = systems.data?.git?.write
  const [proposal, setProposal] = useState<GitProposal | null>(null)
  const [message, setMessage] = useState('')
  const taken = systems.data?.systems.some((s) => s.name === name)
  const nameError = name && !NAME_RE.test(name) ? t('create.nameFormat') : taken ? t('create.nameTaken') : undefined
  const inUse = (s: { described: boolean; inSystems?: string[] }) => (s.described || (s.inSystems?.length ?? 0) > 0 ? 1 : 0)
  const found = (disc.data?.services ?? []).filter((s) => NAME_RE.test(s.name)).sort((a, b) => inUse(a) - inUse(b) || a.name.localeCompare(b.name))

  const submit = () => {
    const services = (picked.length ? picked : ['api']).map((n) => ({ name: n, type: 'http' }))
    const doc = {
      apiVersion: 'kamerton.dev/v1alpha1',
      kind: 'SystemDescription',
      metadata: { name, ...(about.trim() ? { annotations: { 'kamerton.dev/description': about.trim() } } : {}) },
      spec: { services },
    }
    create.mutate(
      { yaml: stringify(doc), message: gitWrite ? message.trim() || `Add system ${name}` : undefined },
      {
      onSuccess: (r) => {
        if (r.proposed && r.proposal) {
          // The system does not exist here yet: it arrives from the repository.
          setProposal(r.proposal)
          return
        }
        onDone()
        navigate(`/systems/${encodeURIComponent(r.id)}`)
      },
      },
    )
  }

  if (proposal)
    return (
      <>
        <DialogHeader>
          <DialogTitle>{t('create.git.title', { name })}</DialogTitle>
          <DialogDescription data-testid="create-proposal">
            {proposal.direct ? (
              t('create.git.committed', { branch: proposal.branch })
            ) : proposal.url ? (
              <>
                {t('create.git.proposed')}{' '}
                <a className="font-medium underline underline-offset-2" href={proposal.url} target="_blank" rel="noreferrer">
                  {proposal.url}
                </a>
              </>
            ) : (
              t('create.git.pushed', { branch: proposal.branch })
            )}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button onClick={onDone}>{t('create.git.close')}</Button>
        </DialogFooter>
      </>
    )

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('create.title')}</DialogTitle>
        <DialogDescription>
          {t('create.description')}
          {gitWrite && <> {t(gitWrite === 'commit' ? 'create.git.hintCommit' : 'create.git.hintPropose')}</>}
        </DialogDescription>
      </DialogHeader>
      <div className="space-y-4">
        <Field label={t('create.name')} htmlFor="sys-name" hint={t('create.nameHint')} error={nameError}>
          <Input id="sys-name" value={name} onChange={(e) => setName(e.target.value.trim())} placeholder="shop" autoFocus />
        </Field>
        <Field label={t('create.about')} htmlFor="sys-about" hint={t('create.optional')}>
          <Input id="sys-about" value={about} onChange={(e) => setAbout(e.target.value)} placeholder={t('create.aboutPlaceholder')} />
        </Field>
        <Field label={t('create.services')} hint={found.length ? t('create.servicesHint') : undefined}>
          {disc.isLoading ? (
            <p className="text-sm text-muted-foreground">{t('create.searching')}</p>
          ) : found.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              {disc.data?.configured ? t('create.noneFound') : t('create.noProm')} {t('create.startsWithApi')}
            </p>
          ) : (
            <div className="max-h-56 space-y-1 overflow-y-auto rounded-md border p-2">
              {found.map((s) => {
                const elsewhere = s.described || (s.inSystems?.length ?? 0) > 0
                return (
                  <label key={s.name} className="flex items-center gap-2 text-sm">
                    <Checkbox checked={picked.includes(s.name)} onCheckedChange={(on) => setPicked((p) => (on ? [...p, s.name] : p.filter((x) => x !== s.name)))} />
                    <span>{s.name}</span>
                    {elsewhere && <span className="text-xs text-muted-foreground">{t('create.elsewhere')}</span>}
                  </label>
                )
              })}
            </div>
          )}
        </Field>
        {gitWrite && (
          <Field label={t('create.git.message')} htmlFor="sys-msg" hint={t('create.optional')}>
            <Input id="sys-msg" value={message} onChange={(e) => setMessage(e.target.value)} placeholder={`Add system ${name || 'shop'}`} maxLength={120} />
          </Field>
        )}
        {create.isError && (
          <p className="text-sm text-destructive">
            {t('create.error', { error: create.error instanceof ApiError && create.error.code === 'not_writable' ? t('create.notWritable') : create.error.message })}
          </p>
        )}
      </div>
      <DialogFooter>
        <Button variant="ghost" onClick={onDone}>
          {t('common.cancel')}
        </Button>
        <Button onClick={submit} disabled={!name || !!nameError || create.isPending}>
          {t('create.submit')}
        </Button>
      </DialogFooter>
    </>
  )
}
