// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Confirmation in the app's own style instead of window.confirm, which blocks
// UI tests and looks foreign. Focus starts on "Cancel", so Enter never
// confirms a deletion by accident.

import { useCallback, useRef, useState, type ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import { useT } from '@/i18n'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'

export interface ConfirmOptions {
  /** A question: "Delete system \"shop\"?" */
  title: ReactNode
  /** What will happen. */
  description?: ReactNode
  confirmLabel?: string
  cancelLabel?: string
  /** Red confirm button (default). */
  destructive?: boolean
}

interface Pending extends ConfirmOptions {
  resolve: (ok: boolean) => void
}

/**
 * Returns the dialog element to render and confirm(), which resolves to true
 * when the user confirms and false when they cancel or close the dialog.
 */
export function useConfirm(): [ReactNode, (opts: ConfirmOptions) => Promise<boolean>] {
  const [pending, setPending] = useState<Pending | null>(null)
  const [open, setOpen] = useState(false)
  const cancelRef = useRef<HTMLButtonElement>(null)
  const { t } = useT()

  const confirm = useCallback(
    (opts: ConfirmOptions) =>
      new Promise<boolean>((resolve) => {
        setPending({ ...opts, resolve })
        // Opened on the next tick: a menu item that triggers it must finish
        // closing (and returning focus) first.
        setTimeout(() => setOpen(true), 0)
      }),
    [],
  )

  const finish = (ok: boolean) => {
    pending?.resolve(ok)
    setOpen(false)
  }

  const destructive = pending?.destructive ?? true
  const element = (
    <Dialog open={open} onOpenChange={(o) => !o && finish(false)}>
      {pending && (
        <DialogContent
          showCloseButton={false}
          data-testid="confirm-dialog"
          onOpenAutoFocus={(e) => {
            e.preventDefault()
            cancelRef.current?.focus()
          }}
        >
          <DialogHeader>
            <DialogTitle className="leading-snug">{pending.title}</DialogTitle>
            {pending.description ? <DialogDescription>{pending.description}</DialogDescription> : <DialogDescription className="sr-only">{t('confirm.title')}</DialogDescription>}
          </DialogHeader>
          <DialogFooter>
            <Button ref={cancelRef} variant="outline" data-testid="confirm-cancel" onClick={() => finish(false)}>
              {pending.cancelLabel ?? t('common.cancel')}
            </Button>
            <Button
              variant={destructive ? 'destructive' : 'default'}
              className={destructive ? 'bg-destructive text-white hover:bg-destructive/90 dark:bg-destructive dark:hover:bg-destructive/90' : undefined}
              data-testid="confirm-ok"
              onClick={() => finish(true)}
            >
              {pending.confirmLabel ?? t('common.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      )}
    </Dialog>
  )
  return [element, confirm]
}
