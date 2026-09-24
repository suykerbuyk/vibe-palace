// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package embedder

import "fmt"

// BehaviourVersion names the embedding regime: what vector this binary's
// embedder produces for a given input under a given model and max_seq_len.
//
// 🔴 BUMP IT IN THE SAME CHANGE THAT ALTERS EMBEDDING OUTPUT FOR THE SAME
// INPUT: a truncation rule or limit, a pooling or normalisation change, the
// defaultMaxSeqLen fallback, a tokenizer option. Cached vectors carry no
// embedder identity of their own (a drawer vector is keyed by
// md5(wing+content)), so without a bump every host keeps its old vectors next
// to the new ones for good — which is what 23bedcc's truncation did. A bump
// makes each host's cache re-embed every project once (the embed cache compares
// Fingerprint against a per-project sidecar).
//
// 1: truncation to maxSeqLen-2 runes, as introduced by 23bedcc.
const BehaviourVersion = 1

// Fingerprint identifies the embedding regime for a model and configured
// max_sequence_length (0 kept as 0: the fallback is covered by
// BehaviourVersion). Two embedders with equal fingerprints produce the same
// vector for the same input, so a cached vector written under one is valid
// under the other.
func Fingerprint(model string, maxSeqLen int) string {
	return fmt.Sprintf("vp-embed behaviour=%d model=%s max_seq_len=%d", BehaviourVersion, model, maxSeqLen)
}
