export interface GateInput {
  targetDatabase: string;
  targetExists: boolean;
  typedConfirmation: string;
  passphraseRequired: boolean;
  passphrase: string;
}

// canRestore: an existing target is only overwritten after typing its exact name (FR-018).
export function canRestore(g: GateInput): boolean {
  if (!g.targetDatabase.trim()) return false;
  if (g.passphraseRequired && !g.passphrase) return false;
  return !g.targetExists || g.typedConfirmation === g.targetDatabase;
}
