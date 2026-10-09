import { useEffect, useState } from "react";

export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export async function request<T>(
  path: string,
  options?: RequestInit,
): Promise<T> {
  const response = await fetch(`/api${path}`, {
    ...options,
    headers: {
      ...(options?.body ? { "Content-Type": "application/json" } : {}),
      ...options?.headers,
    },
  });
  if (!response.ok) {
    const text = await response.text();
    let message = text || response.statusText;
    try {
      const payload: unknown = JSON.parse(text);
      if (
        payload &&
        typeof payload === "object" &&
        "error" in payload &&
        typeof payload.error === "string"
      )
        message = payload.error;
    } catch {
      /* Non-JSON HTTP errors retain their response body. */
    }
    throw new Error(`${response.status}: ${message}`);
  }
  return response.json() as Promise<T>;
}

export function post<T>(path: string, body?: unknown): Promise<T> {
  return request<T>(path, {
    method: "POST",
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
}

export async function getLog(
  runId: string,
  nodeId: string,
  signal?: AbortSignal,
): Promise<string> {
  const response = await fetch(logUrl(runId, nodeId), { signal });
  if (!response.ok)
    throw new Error(
      `${response.status}: ${(await response.text()) || response.statusText}`,
    );
  return response.text();
}

export function logUrl(runId: string, nodeId: string): string {
  return `/api/runs/${encodeURIComponent(runId)}/nodes/${encodeURIComponent(nodeId)}/log`;
}

export function artifactUrl(
  runId: string,
  nodeId: string,
  name: string,
): string {
  return `/api/runs/${encodeURIComponent(runId)}/nodes/${encodeURIComponent(nodeId)}/artifacts/${encodeURIComponent(name)}`;
}

export function href(section: string, id = ""): string {
  return `#/${section}${id ? `/${encodeURIComponent(id)}` : ""}`;
}

export function useRoute() {
  const [hash, setHash] = useState(window.location.hash);
  useEffect(() => {
    const change = () => setHash(window.location.hash);
    window.addEventListener("hashchange", change);
    return () => window.removeEventListener("hashchange", change);
  }, []);
  const [path, query = ""] = hash.replace(/^#\/?/, "").split("?");
  const [section = "projects", encodedId = ""] = path.split("/");
  let id = encodedId;
  try {
    id = decodeURIComponent(encodedId);
  } catch {
    /* Invalid locations render as not found. */
  }
  return {
    section: section || "projects",
    id,
    query: new URLSearchParams(query),
  };
}

export function parseObject<T>(text: string, label: string): Record<string, T> {
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch (error) {
    throw new Error(`${label}: ${errorMessage(error)}`);
  }
  if (!value || typeof value !== "object" || Array.isArray(value))
    throw new Error(`${label} must be a JSON object.`);
  return value as Record<string, T>;
}

export function json(value: unknown): string {
  return JSON.stringify(value, null, 2);
}
export function timestamp(value?: string): string {
  return value ? new Date(value).toLocaleString() : "—";
}
