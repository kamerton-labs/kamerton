// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Change the address of one program of a set. Saving does not require a
// successful check, because a program may be set up later.

import { useState } from 'react'
import { Loader2, RotateCcw, SearchCheck } from 'lucide-react'
import { StatusDot } from '@/components/notifications/parts'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { checkAddress, useResetStackAddress, useSetStackAddress, type CheckResult, type ProgramKind, type StackSet } from '@/lib/api'
import { useT } from '@/i18n'
import { checkText, checkTone, programInfo, stackName } from '@/lib/stacks'

export function CheckLine({ result }: { result: CheckResult }) {
  const { t } = useT()
  return (
    <div role="status" data-testid="address-check-result" className="space-y-0.5">
      <StatusDot tone={checkTone(result.state)}>
        <span className="font-medium text-foreground">{checkText(result.state)}</span>
        {result.version && <span>· {t('shell.version', { v: result.version })}</span>}
        {result.state === 'ok' && <span>· {t('address.latency', { ms: result.latencyMs })}</span>}
      </StatusDot>
      {result.state !== 'ok' && <p className="text-xs text-muted-foreground">{result.message}</p>}
    </div>
  )
}

export default function AddressDialog({ set, kind, onClose }: { set: StackSet; kind: ProgramKind; onClose: () => void }) {
  const program = set.programs.find((p) => p.kind === kind)
  const info = programInfo[kind]
  const { t } = useT()
  const [url, setUrl] = useState(program?.url ?? '')
  const [checked, setChecked] = useState<CheckResult | null>(null)
  const [checking, setChecking] = useState(false)
  const [checkError, setCheckError] = useState('')
  const save = useSetStackAddress()
  const reset = useResetStackAddress()
  const busy = save.isPending || reset.isPending

  const runCheck = async () => {
    setChecking(true)
    setCheckError('')
    try {
      setChecked(await checkAddress(kind, url.trim()))
    } catch (e) {
      setCheckError(e instanceof Error ? e.message : String(e))
    } finally {
      setChecking(false)
    }
  }
  const canReset = set.builtin ? !!program?.overridden : !!program?.url
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg" data-testid="address-dialog">
        <DialogHeader>
          <DialogTitle>
            {t('address.title', { program: info.title, set: stackName(set.id, set.name) })}
          </DialogTitle>
          <DialogDescription>
            {t('address.description')}
            {set.builtin && program?.flagURL ? ` ${t('address.atInstall', { url: program.flagURL })}` : ''}
          </DialogDescription>
        </DialogHeader>
        <form
          id="address-form"
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault()
            save.mutate({ id: set.id, kind, url: url.trim() }, { onSuccess: onClose })
          }}
        >
          <div className="space-y-1.5">
            <Label htmlFor="address-input">{t('address.label')}</Label>
            <div className="flex gap-2">
              <Input
                id="address-input"
                data-testid="address-input"
                className="font-mono"
                spellCheck={false}
                autoComplete="off"
                placeholder={info.example}
                value={url}
                onChange={(e) => {
                  setUrl(e.target.value)
                  setChecked(null)
                }}
              />
              <Button type="button" variant="outline" data-testid="address-check" disabled={!url.trim() || checking} onClick={() => void runCheck()}>
                {checking ? <Loader2 className="animate-spin" /> : <SearchCheck />} {t('status.check')}
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">{t('address.hint', { example: info.example })}</p>
          </div>
          {checked && <CheckLine result={checked} />}
          {checkError && <p className="text-xs text-destructive">{t('address.checkError', { error: checkError })}</p>}
          {save.isError && <p className="text-sm text-destructive">{t('system.saveError', { error: save.error.message })}</p>}
          {reset.isError && <p className="text-sm text-destructive">{t('common.failed', { error: reset.error.message })}</p>}
        </form>
        <DialogFooter className="sm:justify-between">
          {canReset ? (
            <Button
              type="button"
              variant="ghost"
              className="text-muted-foreground"
              data-testid="address-reset"
              disabled={busy}
              onClick={() => reset.mutate({ id: set.id, kind }, { onSuccess: onClose })}
            >
              <RotateCcw /> {set.builtin ? t('address.resetBuiltin') : t('email.disconnect.confirm')}
            </Button>
          ) : (
            <span />
          )}
          <div className="flex gap-2">
            <Button type="button" variant="outline" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" form="address-form" data-testid="address-save" disabled={busy}>
              {save.isPending && <Loader2 className="animate-spin" />} {t('system.save')}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
