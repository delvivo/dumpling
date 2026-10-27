import { describe, expect, it } from "vitest";
import { fromCron, toCron, type Preset } from "./cronPresets";

describe("cron presets", () => {
  const cases: [Preset, string][] = [
    [{ kind: "hourly", minute: 15 }, "15 * * * *"],
    [{ kind: "daily", hour: 2, minute: 30 }, "30 2 * * *"],
    [{ kind: "weekly", weekday: 0, hour: 3, minute: 0 }, "0 3 * * 0"],
  ];
  it.each(cases)("round-trips %o", (p, cron) => {
    expect(toCron(p)).toBe(cron);
    expect(fromCron(cron)).toEqual(p);
  });
  it("keeps unknown expressions raw", () => {
    expect(fromCron("*/5 * * * *")).toEqual({ kind: "cron", expr: "*/5 * * * *" });
    expect(fromCron("CRON_TZ=UTC 0 2 * * *").kind).toBe("cron");
    expect(toCron({ kind: "cron", expr: " 0 1 * * 1-5 " })).toBe("0 1 * * 1-5");
  });
});
