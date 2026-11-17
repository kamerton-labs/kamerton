// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Set of programs a system uses (spec.stack). Empty means the built-in set,
// so choosing the built-in set removes the field.

import { clone, type Description } from './types'

/** ID of the built-in set (internal/stack.BuiltinID). */
export const BUILTIN_STACK = 'main'

/** The set the description asks for ('' = the built-in set). */
export function getStack(desc: Description): string {
  return desc.spec.stack ?? ''
}

/** Chooses a set; the built-in set (or '') removes the field. */
export function setStack(desc: Description, id: string): Description {
  const next = clone(desc)
  const v = id.trim()
  if (v && v !== BUILTIN_STACK) next.spec.stack = v
  else delete next.spec.stack
  return next
}
