// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { afterEach, describe, expect, it } from 'vitest'
import { setLang } from '@/i18n'
import type { FoundProgram, StackComponent, StackSet } from '@/lib/api'
import { parseDescriptionYaml, writeDescriptionYaml } from '@/lib/model/yaml'
import { getStack, setStack } from '@/lib/model/stack'
import { coreHealth, draftFromFound, groupFound, labelsToText, programState, setsInUse, stackBadge, stackName, suggestedLabels, textToLabels } from './stacks'

const comp = (name: StackComponent['name'], over: Partial<StackComponent> = {}): StackComponent => ({
  name,
  configured: true,
  external: false,
  origin: 'bundled',
  healthy: true,
  state: 'ok',
  url: `http://${name}`,
  ...over,
})

afterEach(() => setLang('en', false))

function set(id: string, over: Partial<StackSet> = {}): StackSet {
  return {
    id,
    // Earlier cores send the name of the built-in set in Russian.
    name: id === 'main' ? 'Основной' : id,
    builtin: id === 'main',
    programs: [],
    ruleLabels: {},
    alertmanagerConfigLabels: {},
    health: [comp('prometheus'), comp('alertmanager')],
    usedBy: [],
    warnings: [],
    ...over,
  }
}

describe('sets of programs', () => {
  it('checks Prometheus and Alertmanager of the sets in use only', () => {
    const broken = set('team-b', { health: [comp('prometheus', { healthy: false, state: 'no_answer' }), comp('alertmanager')] })
    expect(coreHealth([set('main', { usedBy: ['shop'] }), broken]).state).toBe('ok')
    const h = coreHealth([set('main', { usedBy: ['shop'] }), { ...broken, usedBy: ['billing'] }])
    expect(h.state).toBe('problem')
    expect(h.problem).toBe('Prometheus not responding (set "team-b")')
    // No system yet: the built-in set counts.
    expect(setsInUse([set('main'), broken]).map((s) => s.id)).toEqual(['main'])
    expect(coreHealth([set('main', { health: [comp('prometheus', { configured: false, healthy: null })] })]).problem).toContain('Prometheus is not connected:')
    expect(coreHealth(undefined).state).toBe('unknown')
  })

  it('describes a program state in plain words', () => {
    expect(programState(undefined)).toEqual({ tone: 'muted', text: 'not connected' })
    expect(programState(comp('grafana', { healthy: false, state: 'wrong_program' }))).toEqual({ tone: 'bad', text: 'a different program answers' })
    expect(programState(comp('loki', { healthy: false, state: 'not_ready' })).tone).toBe('warn')
  })

  it('edits labels as name=value lines', () => {
    expect(textToLabels('release=kps\n team = a \n\n')).toEqual({ labels: { release: 'kps', team: 'a' } })
    expect(textToLabels('release').error).toContain('name=value')
    expect(labelsToText({ a: '1', b: '2' })).toBe('a=1\nb=2')
  })

  it('takes labels from a found operator selector, without Kamerton’s own', () => {
    const found: FoundProgram = {
      kind: 'alertmanager',
      namespace: 'monitoring',
      service: 'alertmanager-operated',
      url: 'http://alertmanager-operated.monitoring.svc:9093',
      healthy: true,
      state: 'ok',
      message: 'Running',
      usedBy: [],
      operator: { kind: 'Alertmanager', namespace: 'monitoring', name: 'kps', labels: { 'kamerton.dev/managed': 'true', team: 'a' }, selectsAll: false, selectsNone: false, hasExpressions: false, namespaces: '' },
    }
    expect(suggestedLabels(found)).toEqual({ team: 'a' })
    const prom: FoundProgram = { ...found, kind: 'prometheus', service: 'prometheus-operated', url: 'http://prometheus-operated.monitoring.svc:9090', operator: { ...found.operator!, kind: 'Prometheus', labels: { release: 'kps' } } }
    const d = draftFromFound([prom, found])
    expect(d.urls).toEqual({ prometheus: prom.url, alertmanager: found.url })
    expect(d.ruleLabels).toEqual({ release: 'kps' })
    expect(d.alertmanagerConfigLabels).toEqual({ team: 'a' })
    expect(d.labelsNote).toContain('Prometheus monitoring/kps')
    expect(groupFound([found, prom]).map((g) => g.kind)).toEqual(['prometheus', 'alertmanager'])
  })

  it('shows the set name of a system only when there are several sets', () => {
    expect(stackBadge(1, 'Main')).toBeNull()
    expect(stackBadge(2, 'Team B')).toBe('Team B')
  })

  it('names the built-in set from the dictionary, whatever name the core sends', () => {
    expect(stackName('main', 'Основной')).toBe('Main')
    expect(stackName('team-b', 'Team B')).toBe('Team B')
    expect(stackName('team-c', '')).toBe('team-c')
    setLang('ru', false)
    expect(stackName('main', 'Main')).toBe('Основной')
  })

  it('writes spec.stack into the description; the built-in set removes it', () => {
    const text = 'apiVersion: kamerton.dev/v1alpha1\nkind: SystemDescription\nmetadata:\n  name: shop\nspec:\n  # services\n  services:\n    - name: api\n      type: http\n'
    const parsed = parseDescriptionYaml(text)
    if (!parsed.ok) throw new Error(parsed.error)
    const withSet = setStack(parsed.desc, 'team-b')
    expect(getStack(withSet)).toBe('team-b')
    expect(getStack(parsed.desc)).toBe('')
    const written = writeDescriptionYaml(text, withSet)
    expect(written).toContain('stack: team-b')
    expect(written).toContain('# services')
    expect('stack' in setStack(withSet, 'main').spec).toBe(false)
  })
})
