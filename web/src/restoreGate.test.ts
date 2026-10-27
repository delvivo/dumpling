import { describe, expect, it } from "vitest";
import { canRestore } from "./restoreGate";

const base = { targetDatabase: "shop_copy", targetExists: false, typedConfirmation: "", passphraseRequired: false, passphrase: "" };

describe("canRestore", () => {
  it("allows a new target without confirmation", () => expect(canRestore(base)).toBe(true));
  it("blocks an empty target name", () => expect(canRestore({ ...base, targetDatabase: " " })).toBe(false));
  it("requires the exact name for an existing target", () => {
    const g = { ...base, targetExists: true };
    expect(canRestore(g)).toBe(false);
    expect(canRestore({ ...g, typedConfirmation: "shop_cop" })).toBe(false);
    expect(canRestore({ ...g, typedConfirmation: "SHOP_COPY" })).toBe(false);
    expect(canRestore({ ...g, typedConfirmation: "shop_copy" })).toBe(true);
  });
  it("requires a passphrase when needed", () => {
    expect(canRestore({ ...base, passphraseRequired: true })).toBe(false);
    expect(canRestore({ ...base, passphraseRequired: true, passphrase: "x" })).toBe(true);
  });
});
