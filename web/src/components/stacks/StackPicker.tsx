// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// A system's set of programs, chosen in its settings (the canvas panel).
// Shown only when more than one set exists: with one set there is nothing
// to choose.

import { useState } from 'react'
import { Check, Layers } from 'lucide-react'
import { Link } from 'react-router'
import { cn } from 'cn'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { useStacks, type StackSet } from '@/lib/api'
import { useEditor } from '@/lib/editor'
import { BUILTIN_STACK, getStack, setStack } from '@/lib/model/stack'
import { useT } from '@/i18n'
import { programInfo, stackName } from '@/lib/stacks'

function StackDialog({ sets, onClose }: { sets: StackSet[]; onClose: () => void }) {
  const { desc, edit, readOnly } = useEditor()
  const { t } = useT()
  const current = getStack(desc) || BUILTIN_STACK
  const [chosen, setChosen] = useState(sets.some((s) => s.id === current) ? current : BUILTIN_STACK)
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md" data-testid="stack-picker-dialog">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Layers className="size-4" /> {t('systems.stackTitle')}
          </DialogTitle>
          <DialogDescription>{t('stackPicker.description')}</DialogDescription>
        </DialogHeader>
        <div className="space-y-1.5" role="radiogroup" aria-label={t('stackPicker.sets')}>
          {sets.map((s) => {
            const prom = s.programs.find((p) => p.kind === 'prometheus')?.url
            return (
              <button
                key={s.id}
                type="button"
                role="radio"
                aria-checked={chosen === s.id}
                data-testid={`stack-option-${s.id}`}
                onClick={() => setChosen(s.id)}
                className={cn(
                  'flex w-full items-start gap-2.5 rounded-lg border p-3 text-left outline-none focus-visible:ring-3 focus-visible:ring-ring/50',
                  chosen === s.id ? 'border-foreground/30 bg-muted/60' : 'hover:bg-muted/40',
                )}
              >
                <Check className={cn('mt-0.5 size-4 shrink-0', chosen === s.id ? 'opacity-100' : 'opacity-0')} />
                <span className="min-w-0">
                  <span className="block text-sm font-medium">
                    {stackName(s.id, s.name)}
                    {s.builtin && <span className="font-normal text-muted-foreground"> · {t('stackBlock.main')}</span>}
                  </span>
                  <span className="block truncate font-mono text-xs text-muted-foreground">
                    {programInfo.prometheus.title}: {prom || t('tg.status.none')}
                  </span>
                </span>
              </button>
            )
          })}
        </div>
        <DialogFooter className="sm:justify-between">
          <Button asChild variant="ghost" className="text-muted-foreground">
            <Link to="/status">{t('stackPicker.statusLink')}</Link>
          </Button>
          <Button
            data-testid="stack-picker-done"
            disabled={readOnly}
            onClick={() => {
              edit((d) => setStack(d, chosen))
              onClose()
            }}
          >
            {t('common.done')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** Row in the canvas panel: "Program set: Team B". */
export default function StackPicker() {
  const { desc } = useEditor()
  const stacks = useStacks()
  const { t } = useT()
  const [open, setOpen] = useState(false)
  const sets = stacks.data?.sets ?? []
  const asked = getStack(desc)
  const current = sets.find((s) => s.id === (asked || BUILTIN_STACK))
  if (sets.length < 2 && current) return null
  if (sets.length < 2 && !asked) return null
  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        data-testid="stack-picker"
        className="flex w-full items-center gap-2 px-1.5 py-1 text-left text-xs text-muted-foreground hover:text-foreground"
        title={t('stackPicker.hint')}
      >
        <Layers className="size-3.5 shrink-0" />
        <span className="min-w-0 truncate">
          {t('stackPicker.row')}{' '}
          {current ? stackName(current.id, current.name) : <span className="text-warn">{t('stackPicker.missing', { name: asked })}</span>}
        </span>
      </button>
      {open && <StackDialog sets={sets} onClose={() => setOpen(false)} />}
    </>
  )
}
