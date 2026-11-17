// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { useT } from '@/i18n'
import { useSchema, type Issue } from '@/lib/api'
import YamlEditor from './YamlEditor'

interface Props {
  text: string
  onChange: (text: string) => void
  issues: Issue[]
  readOnly?: boolean
}

export default function YamlView({ text, onChange, issues, readOnly }: Props) {
  const schema = useSchema()
  const { t } = useT()
  return (
    <div className="flex h-full flex-col">
      <p className="border-b px-4 py-1.5 text-xs text-muted-foreground">
        {t('yaml.intro')}
      </p>
      <YamlEditor value={text} onChange={onChange} schema={schema.data} issues={issues} readOnly={readOnly} className="min-h-0 flex-1 overflow-hidden [&_.cm-editor]:h-full" />
    </div>
  )
}
