// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { useState, type FormEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { CircleAlert, LogIn } from 'lucide-react'
import { Navigate, useNavigate, useSearchParams } from 'react-router'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import LangSwitch from '@/components/LangSwitch'
import { PasswordInput } from '@/components/notifications/parts'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useT } from '@/i18n'
import { useSession } from '@/lib/api'
import { login, LoginError, loginErrorMessage, safeNext } from '@/lib/auth'

export default function LoginPage() {
  const [params] = useSearchParams()
  const next = safeNext(params.get('next'))
  const session = useSession()
  const qc = useQueryClient()
  const { t } = useT()
  const navigate = useNavigate()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  // Nothing to do here when login is off or the session is already there.
  if (session.data && (!session.data.enabled || session.data.authenticated)) return <Navigate to={next} replace />

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!username || !password) {
      setError(t('login.required'))
      return
    }
    setBusy(true)
    setError(null)
    try {
      await login(username, password)
      // Data fetched without a session (401s) must not linger.
      qc.clear()
      void navigate(next, { replace: true })
    } catch (err) {
      setError(err instanceof LoginError ? err.message : loginErrorMessage(0))
      setPassword('')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex min-h-svh items-center justify-center bg-muted/30 p-4">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <div className="mb-2 flex items-center gap-2">
            <img src="/favicon.svg" alt="" className="size-6" />
            <span className="text-base font-semibold">Kamerton</span>
            <span className="ml-auto">
              <LangSwitch />
            </span>
          </div>
          <CardTitle>{t('login.title')}</CardTitle>
          <CardDescription>{t('login.description')}</CardDescription>
        </CardHeader>
        <CardContent>
          <form className="space-y-4" onSubmit={(e) => void submit(e)} noValidate>
            <div className="space-y-1.5">
              <Label htmlFor="login-user">{t('login.user')}</Label>
              <Input
                id="login-user"
                data-testid="login-user"
                name="username"
                autoComplete="username"
                autoFocus
                required
                value={username}
                onChange={(e) => setUsername(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="login-password">{t('login.password')}</Label>
              <PasswordInput
                id="login-password"
                data-testid="login-password"
                name="password"
                autoComplete="current-password"
                required
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            </div>
            {error && (
              <Alert variant="destructive" data-testid="login-error">
                <CircleAlert />
                <AlertDescription>{error}</AlertDescription>
              </Alert>
            )}
            <Button type="submit" className="w-full" data-testid="login-submit" disabled={busy}>
              <LogIn /> {busy ? t('login.busy') : t('login.submit')}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
