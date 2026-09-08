// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

// DecisionRoom is the palace room every decision drawer is filed into. It is a
// PATH SEGMENT — the {room} in palace/{project}/drawers/{wing}/{room}/drawers.jsonl
// — and it is FIXED rather than classified from the drawer's content: a decision
// is filed here because the writer knows it is a decision, not because a
// classifier put it here.
//
// It is one exported symbol precisely because it would otherwise be a bare
// "decisions" literal at three call sites across three tasks — the writer that
// files the drawer, the resolver that defaults an unqualified query to this
// room, and the query that reads it back. A drift in any ONE of them does not
// fail: it yields an empty default query, which is indistinguishable at the
// wire from an honestly-empty palace. The reader sees "no decisions recorded"
// and believes it. A constant makes the three agree by construction.
//
// The symbol lives on the capture side rather than on the drawer type because
// storage.Drawer has no Room field at all: the room reaches storage only as an
// argument to AppendDrawers, so there is no drawer-shaped place to hang it.
const DecisionRoom = "decisions"
