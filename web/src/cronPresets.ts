export type Preset =
  | { kind: "hourly"; minute: number }
  | { kind: "daily"; hour: number; minute: number }
  | { kind: "weekly"; weekday: number; hour: number; minute: number } // 0 = Sunday
  | { kind: "cron"; expr: string };

export function toCron(p: Preset): string {
  switch (p.kind) {
    case "hourly": return `${p.minute} * * * *`;
    case "daily": return `${p.minute} ${p.hour} * * *`;
    case "weekly": return `${p.minute} ${p.hour} * * ${p.weekday}`;
    case "cron": return p.expr.trim();
  }
}

// fromCron recognises expressions produced by toCron; anything else stays raw cron.
export function fromCron(expr: string): Preset {
  const f = expr.trim().split(/\s+/);
  const n = (s: string) => (/^\d+$/.test(s) ? Number(s) : NaN);
  if (f.length === 5 && !Number.isNaN(n(f[0]))) {
    const [m, h, dom, mon, dow] = f;
    if (h === "*" && dom === "*" && mon === "*" && dow === "*") return { kind: "hourly", minute: n(m) };
    if (!Number.isNaN(n(h)) && dom === "*" && mon === "*") {
      if (dow === "*") return { kind: "daily", hour: n(h), minute: n(m) };
      if (!Number.isNaN(n(dow))) return { kind: "weekly", weekday: n(dow), hour: n(h), minute: n(m) };
    }
  }
  return { kind: "cron", expr };
}
