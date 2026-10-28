export interface GateInput {
  targetDatabase: string; // database name, or the folder name for a physical (directory) restore
  targetExists: boolean;
  typedConfirmation: string;
  passphraseRequired: boolean;
  passphrase: string;
  chainError?: string; // a link the backup depends on is missing or corrupted
  physical?: boolean;
  targetPathOk?: boolean;
}

// canRestore: an existing target is only overwritten after typing its exact name (FR-018); a
// broken chain never restores (FR-011); a directory restore needs an accepted empty folder.
export function canRestore(g: GateInput): boolean {
  if (!g.targetDatabase.trim()) return false;
  if (g.chainError) return false;
  if (g.passphraseRequired && !g.passphrase) return false;
  if (g.physical) return !!g.targetPathOk;
  return !g.targetExists || g.typedConfirmation === g.targetDatabase;
}
