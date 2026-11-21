// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

// The closed tables behind vault-write-outside-funnel's typed taint (vault_taint.go).
// Each is a CLAIM about the tree: a row that stops being true is reported (STALE)
// and one that is missing is reported (UNLISTED / a new finding), so none can rot.

const (
	pathStorage   = modulePath + "/internal/storage"
	pathArchive   = modulePath + "/internal/archive"
	pathMemory    = modulePath + "/internal/memory"
	pathHook      = modulePath + "/internal/hook"
	pathIngest    = modulePath + "/internal/ingest"
	pathWrapstate = modulePath + "/internal/wrapstate"
	pathVaultlock = modulePath + "/internal/vaultlock"
	pathContext   = modulePath + "/internal/context"
	pathVplog     = modulePath + "/internal/vplog"
	pathSurface   = modulePath + "/internal/surface"
)

// vaultSourceType names one type whose members are vault-path SOURCES, keyed by
// FULL import path. Allow lists the members (fields, or methods whose first result
// is the path) that ARE sources. Reject lists the root-like members deliberately
// NOT sources, each with its reason.
//
// REVERSE-SET CHECK: every member of a listed type whose name ends in Root, File,
// Dir or Path and whose type (field) or first result (method) is string MUST be in
// Allow or Reject. A new one is reported as sourceaudit.vaultSources/UNLISTED, so a
// vault accessor added tomorrow cannot silently be a path the rule does not trust.
// The importer stubs are built from these rows, keyed by the same full path.
type vaultSourceType struct {
	ImportPath string
	Type       string
	Allow      []string
	Reject     map[string]string
}

var vaultSources = []vaultSourceType{
	{ImportPath: pathStorage, Type: "Vault",
		Allow: []string{
			"Root",
			"AbsorbedFile", "CommitLogAnchorFile", "CommitLogFile", "CommitMsgFile", "DocFile", "DrawerDir", "DrawerFile",
			"EmbedCacheDir", "ImportsDir", "IndexCommitLockPath", "IndexDir", "IndexGenerationPath", "IndexLocksDir",
			"IndexRenamePendingDir", "IndexRenamePendingPath", "IndexRootDir", "IndexRunCompletedPath", "IndexRunHolderPath",
			"IndexRunLockPath", "IndexTombstonePath", "IngestedArchivesFile", "IterationSummaryFile", "IterationsFile",
			"KGEntitiesFile", "KGTriplePath", "KGTriplesDir", "KnowledgeFile", "LearningsDir", "LocalDir", "MemoryDir",
			"MemoryFile", "ProjectDir", "ResumeFile", "SessionDir", "SessionFile", "TaskCancelledDir", "TaskDoneDir",
			"TaskFile", "TasksDir", "VaultLocalDir", "WorkflowFile", "kgMigrationLockPath", "resolveTaskFile",
		},
		Reject: map[string]string{
			"SessionRelPath": "returns a vault-RELATIVE path (the form persisted in note_path), never a write destination",
		}},
	{ImportPath: pathStorage, Type: "CheckoutRebind", Allow: []string{"VaultRoot", "VaultPath"}},
	{ImportPath: pathArchive, Type: "CreateOptions", Allow: []string{"VaultRoot"},
		Reject: map[string]string{"SourcePath": "the host transcript source file: read, never written"}},
	{ImportPath: pathArchive, Type: "RepairOptions", Allow: []string{"VaultRoot"}},
	{ImportPath: pathMemory, Type: "Options", Allow: []string{"VaultRoot"},
		Reject: map[string]string{"NativeDir": "the host's Claude-native memory directory, outside the vault"}},
	{ImportPath: pathHook, Type: "RunOptions", Allow: []string{"VaultRoot"},
		Reject: map[string]string{"ClaimDir": "the checkout's .vibe-palace claim directory, never the vault (hook.WriteClaim in the vault-dir allowlist)"}},
	{ImportPath: pathIngest, Type: "RunOptions", Allow: []string{"VaultRoot"}},
	{ImportPath: pathWrapstate, Type: "CollectInput", Allow: []string{"VaultRoot", "IterationsPath", "TasksDir"},
		Reject: map[string]string{"ProjectRoot": "the local project checkout root (anchors, git), not the vault"}},
	{ImportPath: pathVaultlock, Type: "Held", Allow: []string{"Root"}},
	{ImportPath: pathContext, Type: "Resolver", Allow: []string{"VaultRoot", "VaultPath", "vaultRoot"},
		Reject: map[string]string{"resolveByPath": "returns a resource's text and its source label, not a filesystem path"}},
}

// seedPkgs are the packages whose package-level functions' vault-named string
// PARAMETERS seed the taint (vaultRoot, vaultPath, vaultDir, destRoot): the
// vault-handling packages (the vault-dir-creation scan set, plus the packages the
// census found holding such parameters) and cmd/vp. A local variable or a named
// result is NEVER a seed, whatever its name.
var seedPkgs = map[string]bool{
	"archive": true, "absorb": true, "reconcile": true, "migrate": true, "vaultaudit": true,
	"surface": true, "storage": true, "vaultfs": true, "atomicfile": true, "vaultlock": true,
	"indexstore": true, "search": true, "templates": true, "onboard": true, "tools": true,
	"memory": true, "hook": true, "commands": true, "palace": true, "check": true, "departure": true,
	"departedpath": true, "projectdir": true, "project": true, "planscan": true, "wrapstate": true,
	"capture": true, "enrichment": true, "context": true, "testutil": true,
}

var seedParamNames = map[string]bool{"vaultRoot": true, "vaultPath": true, "vaultDir": true, "destRoot": true}

// designatedRootFuncs: `root` is ambiguous in this tree (worktree roots, task ids,
// project roots, graph roots), so it seeds ONLY in these exact functions. Key
// `<package name>.<Func or Type.Method>`; the value is the reason.
var designatedRootFuncs = map[string]string{
	"surface.vaultManifestPath":     "the vault manifest path: root is the vault root",
	"surface.VaultManifestPath":     "the vault manifest path: root is the vault root",
	"surface.ReadFormat":            "reads the vault's format stamp: root is the vault root",
	"surface.ReadVaultManifest":     "reads the vault manifest: root is the vault root",
	"surface.WriteVaultManifest":    "writes the vault manifest: root is the vault root",
	"surface.WriteFormat":           "writes the vault's format stamp: root is the vault root",
	"surface.checkFormatCompatible": "checks the vault's format stamp: root is the vault root",
	"storage.NewVault":              "constructs a Vault: root is the vault root",
}

// designatedParams: individual (function, parameter) seeds, the narrow sibling of
// designatedRootFuncs. Key `<package name>.<Func>.<param>`.
//
// writeCloneMarker.scratch is deliberately NOT here: it is the SCRATCH clone's
// checkout, whose .git receives the marker before the clone is a vault, so the
// write is not into a vault. That is a stated non-detection.
var designatedParams = map[string]string{
	"storage.finishClone.path": "the finished clone's vault root: the marker removed there is the clone marker under .git",
}

// blessedSinkPkgs are the funnel family: calling them is the goal, so they are
// never wrappers and the wrapper summary does not traverse them.
var blessedSinkPkgs = map[string]bool{
	modulePath + "/internal/atomicfile": true,
	modulePath + "/internal/vaultfs":    true,
}

// blessedSinkFuncs are individual funnel functions outside those packages
// (`<package name>.<Func or Type.Method>`).
var blessedSinkFuncs = map[string]bool{
	"storage.appendUnderLock": true, "storage.Vault.appendUnderLock": true,
	// The ROOT-KEYED lock family (ADR-003): these create the advisory lock files under
	// <vault>/.vp-locks from a path they hash, and the funnel cannot take a lock to
	// create its own lock file. With wrapper summaries following derived locals, every
	// function that locks (the whole write layer) would otherwise become a "wrapper";
	// calling the lock family is the goal, exactly like atomicfile. Their own raw
	// opens stay reported as direct hits (baselined: openLockFileNoGC, sweepDeadLocks,
	// acquireGCShared, acquireGCExclusiveBounded). The FILE-keyed openers
	// (TryAcquireFile, AcquireFileWithTimeout) are NOT blessed: they are the enumerated
	// edges in funnelAllowedEdges.
	"vaultlock.Acquire": true, "vaultlock.TryAcquire": true, "vaultlock.AcquireHeld": true, "vaultlock.AcquirePair": true,
	"vaultlock.AcquireWithTimeout": true, "vaultlock.SweepDeadLocks": true, "vaultlock.acquireByKey": true,
	"vaultlock.acquireSecondLeg": true, "vaultlock.openLockFile": true, "vaultlock.openLockFileNoGC": true,
	"vaultlock.acquireGCShared": true, "vaultlock.acquireGCExclusiveBounded": true, "vaultlock.sweepDeadLocks": true,
}

// allowedEdge is one ENUMERATED caller->callee call edge accepted although the
// callee reaches a raw sink with a vault-derived argument. There is no global
// callee exemption: a new caller of the same callee is a finding.
type allowedEdge struct {
	count  int
	reason string
}

// Key: `<caller finding id>|<callee import path>.<callee name>`.
var funnelAllowedEdges = map[string]allowedEdge{
	"indexstore.Lock|" + pathVaultlock + ".AcquireFileWithTimeout":          {1, "host-local advisory lock file under the git-ignored palace/.local"},
	"indexstore.LockLifecycle|" + pathVaultlock + ".AcquireFileWithTimeout": {1, "host-local advisory lock file under the git-ignored palace/.local"},
	"indexstore.runLockHeld|" + pathVaultlock + ".TryAcquireFile":           {1, "host-local advisory lock file under the git-ignored palace/.local"},
	"indexstore.tryRunLockFile|" + pathVaultlock + ".TryAcquireFile":        {1, "host-local advisory lock file under the git-ignored palace/.local"},
	"main.initLoggingFor|" + pathVplog + ".Init":                            {1, "vp.log under the vault's git-ignored local directory (v.VaultLocalDir()+\"/vp.log\")"},
}

// typecheckAccounted pins, per package import path, the sorted multiset of
// UNEXPECTED type-error fingerprints the fake-importer check legitimately produces
// (see classifyIssues). A fingerprint present and not listed is reported NEW, one
// listed and absent is reported STALE, so replacing one expected error with a
// different unexpected one at the same count is never silent.
var typecheckAccounted = map[string]accountedIssues{
	modulePath + "/internal/atomicfile": {reason: "build-tag twins (_unix/_windows or _go/_ort files) type-checked together in one package", fingerprints: []string{
		"declaration-conflict|rename_other.go|retryableRenameErr",
		"declaration-conflict|rename_windows.go|retryableRenameErr",
	}},
	modulePath + "/internal/check": {reason: "build-tag twins (_unix/_windows or _go/_ort files) type-checked together in one package", fingerprints: []string{
		"declaration-conflict|codex_orphans_unix.go|fileUID",
		"declaration-conflict|codex_orphans_windows.go|fileUID",
	}},
	modulePath + "/internal/detachlaunch": {reason: "build-tag twins (_unix/_windows or _go/_ort files) type-checked together in one package", fingerprints: []string{
		"declaration-conflict|launch_unix.go|markInheritedFDsCloseOnExec",
		"declaration-conflict|launch_unix.go|setDetached",
		"declaration-conflict|launch_unix.go|setDetachedFallback",
		"declaration-conflict|launch_windows.go|markInheritedFDsCloseOnExec",
		"declaration-conflict|launch_windows.go|setDetached",
		"declaration-conflict|launch_windows.go|setDetachedFallback",
	}},
	modulePath + "/internal/embedder": {reason: "build-tag twins (_unix/_windows or _go/_ort files) type-checked together in one package", fingerprints: []string{
		"declaration-conflict|session_go.go|ORTAvailable",
		"declaration-conflict|session_go.go|installTruncation",
		"declaration-conflict|session_go.go|newHugotSession",
		"declaration-conflict|session_ort.go|ORTAvailable",
		"declaration-conflict|session_ort.go|newHugotSession",
		"declaration-conflict|truncate_ort.go|installTruncation",
	}},
	modulePath + "/internal/ingest": {reason: "build-tag twins (_unix/_windows or _go/_ort files) type-checked together in one package", fingerprints: []string{
		"declaration-conflict|freespace_unix.go|newPlatformFreeSpace",
		"declaration-conflict|freespace_unix.go|platformFreeSpace",
		"declaration-conflict|freespace_windows.go|Free",
		"declaration-conflict|freespace_windows.go|newPlatformFreeSpace",
		"declaration-conflict|freespace_windows.go|platformFreeSpace",
	}},
	modulePath + "/internal/tools": {reason: "a knock-on of an unmodelled import's type (a field of a type that came from a fake import)", fingerprints: []string{
		"no-member|context_tools.go|State",
	}},
	modulePath + "/internal/vaultlock": {reason: "build-tag twins (_unix/_windows or _go/_ort files) type-checked together in one package", fingerprints: []string{
		"declaration-conflict|flock_unix.go|flockExclusive",
		"declaration-conflict|flock_unix.go|flockShared",
		"declaration-conflict|flock_unix.go|flockTryExclusive",
		"declaration-conflict|flock_unix.go|flockTryShared",
		"declaration-conflict|flock_unix.go|funlock",
		"declaration-conflict|flock_windows.go|flockExclusive",
		"declaration-conflict|flock_windows.go|flockShared",
		"declaration-conflict|flock_windows.go|flockTryExclusive",
		"declaration-conflict|flock_windows.go|flockTryShared",
		"declaration-conflict|flock_windows.go|funlock",
	}},
}

type accountedIssues struct {
	fingerprints []string
	reason       string
}
