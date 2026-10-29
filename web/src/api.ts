export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string, public body: any) {
    super(message);
  }
}

export async function api<T = any>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch("/api" + path, {
    method,
    headers: { "X-Requested-With": "dumpling", ...(body !== undefined ? { "Content-Type": "application/json" } : {}) },
    body: body !== undefined ? JSON.stringify(body) : undefined,
    credentials: "same-origin",
  });
  if (res.status === 401 && !path.startsWith("/login") && !path.startsWith("/setup")) {
    window.dispatchEvent(new Event("dumpling:unauthorized"));
  }
  const text = await res.text();
  const data = text && res.headers.get("Content-Type")?.includes("json") ? JSON.parse(text) : text;
  if (!res.ok) throw new ApiError(res.status, data?.code ?? "error", data?.error ?? res.statusText, data);
  return data as T;
}

export const get = <T = any>(p: string) => api<T>("GET", p);
export const post = <T = any>(p: string, b: unknown = {}) => api<T>("POST", p, b);
export const put = <T = any>(p: string, b: unknown) => api<T>("PUT", p, b);
export const del = <T = any>(p: string, b?: unknown) => api<T>("DELETE", p, b);

export function bytes(n?: number | null): string {
  if (n == null) return "–";
  const u = ["B", "KiB", "MiB", "GiB", "TiB"];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return `${n.toFixed(i ? 1 : 0)} ${u[i]}`;
}

export const when = (s?: string) => (s ? new Date(s).toLocaleString() : "–");
