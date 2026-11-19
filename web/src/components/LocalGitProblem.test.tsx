// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it } from 'vitest'
import { setLang } from '@/i18n'
import { ApiError, gitFailure } from '@/lib/api'
import LocalGitProblem from './LocalGitProblem'

afterEach(() => setLang('en'))

describe('LocalGitProblem', () => {
  it('says a known problem in the language of the interface, with git under it', () => {
    setLang('ru')
    const html = renderToStaticMarkup(<LocalGitProblem problem="auth" detail="fatal: could not read Username" />)
    expect(html).toContain('Нет доступа к репозиторию из вашего git')
    expect(html).toContain('fatal: could not read Username')
  })

  it('shows git as it is when the problem is not a known one', () => {
    const html = renderToStaticMarkup(<LocalGitProblem detail="error: something odd" />)
    expect(html).toContain('error: something odd')
  })
})

describe('gitFailure', () => {
  it('takes the problem and git words from the answer', () => {
    const e = new ApiError(422, 'git_failed', 'git: x', { problem: 'behind', detail: '! [rejected]' })
    expect(gitFailure(e)).toEqual({ problem: 'behind', detail: '! [rejected]', message: 'git: x' })
  })

  it('falls back to the message', () => {
    expect(gitFailure(new Error('down'))).toEqual({ message: 'down' })
    expect(gitFailure(null)).toBeNull()
  })
})
