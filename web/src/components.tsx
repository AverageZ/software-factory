import type { ReactNode } from "react";
import { json, timestamp } from "./api";
import type { Project, Run, Value } from "./types";

export function ErrorNotice({ message }: { message: string }) {
  return message ? (
    <div className="notice error" role="alert">
      {message}
    </div>
  ) : null;
}

export function PageHeader({
  title,
  description,
  children,
}: {
  title: string;
  description?: string;
  children?: ReactNode;
}) {
  return (
    <header className="page-header">
      <div>
        <h1>{title}</h1>
        {description && <p>{description}</p>}
      </div>
      {children && <div className="actions">{children}</div>}
    </header>
  );
}

export function Status({ status }: { status: string }) {
  return <span className={`status status-${status}`}>{status}</span>;
}

export function JsonField({
  label,
  value,
  onChange,
  help,
  rows = 8,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  help?: string;
  rows?: number;
}) {
  return (
    <label className="field">
      <span>{label}</span>
      <textarea
        className="code-input"
        rows={rows}
        spellCheck={false}
        value={value}
        onChange={(event) => onChange(event.target.value)}
      />
      {help && <small>{help}</small>}
    </label>
  );
}

export function RunTable({ runs, projects }: { runs: Run[]; projects: Project[] }) {
  if (!runs.length) return <p className="empty">No recorded runs.</p>;
  return (
    <div className="table-scroll">
      <table>
        <thead>
          <tr>
            <th>Run</th>
            <th>Project</th>
            <th>Status</th>
            <th>Started</th>
            <th>Updated</th>
          </tr>
        </thead>
        <tbody>
          {[...runs]
            .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
            .map((run) => (
              <tr key={run.id}>
                <td>
                  <a href={`#/runs/${encodeURIComponent(run.id)}`}>
                    <code>{run.id.slice(0, 12)}</code>
                  </a>
                  {run.parentRunId && (
                    <span className="muted"> · child run</span>
                  )}
                </td>
                <td>
                  {projects.find((project) => project.id === run.projectId)
                    ?.name || run.projectId}
                </td>
                <td>
                  <Status status={run.status} />
                </td>
                <td>{timestamp(run.createdAt)}</td>
                <td>{timestamp(run.updatedAt)}</td>
              </tr>
            ))}
        </tbody>
      </table>
    </div>
  );
}

export function TypedValues({
  declarations,
  values,
  onChange,
}: {
  declarations: Record<string, string>;
  values: Record<string, string>;
  onChange: (values: Record<string, string>) => void;
}) {
  const entries = Object.entries(declarations);
  if (!entries.length)
    return <p className="muted">This workflow declares no run inputs.</p>;
  return (
    <div className="stack">
      {entries.map(([name, type]) => (
        <label className="field" key={name}>
          <span>
            {name} <code>{type}</code>
          </span>
          {type === "boolean" ? (
            <select
              value={values[name] ?? ""}
              onChange={(event) =>
                onChange({ ...values, [name]: event.target.value })
              }
            >
              <option value="">Choose a value</option>
              <option value="true">true</option>
              <option value="false">false</option>
            </select>
          ) : type === "string" ? (
            <input
              value={values[name] ?? ""}
              onChange={(event) =>
                onChange({ ...values, [name]: event.target.value })
              }
            />
          ) : (
            <textarea
              className="code-input"
              rows={type === "number" ? 1 : 4}
              value={values[name] ?? ""}
              onChange={(event) =>
                onChange({ ...values, [name]: event.target.value })
              }
              spellCheck={false}
              placeholder={
                type === "number"
                  ? "JSON number"
                  : type === "array"
                    ? "JSON array"
                    : "JSON object"
              }
            />
          )}
          {type !== "string" && (
            <small>
              Enter a valid{" "}
              {type === "number" || type === "boolean" || type === "array"
                ? type
                : "object"}{" "}
              JSON value.
            </small>
          )}
        </label>
      ))}
    </div>
  );
}

export function valueText(
  values: Record<string, Value>,
): Record<string, string> {
  return Object.fromEntries(
    Object.entries(values).map(([name, entry]) => [
      name,
      entry.type === "string" && typeof entry.value === "string"
        ? entry.value
        : json(entry.value),
    ]),
  );
}

export function typedValues(
  declarations: Record<string, string>,
  texts: Record<string, string>,
): Record<string, Value> {
  return Object.fromEntries(
    Object.entries(declarations).map(([name, type]) => {
      const text = texts[name] ?? "";
      let value: unknown = text;
      if (type !== "string") {
        try {
          value = JSON.parse(text);
        } catch {
          throw new Error(
            `Input “${name}” requires a valid ${type} JSON value.`,
          );
        }
        const valid =
          type === "array"
            ? Array.isArray(value)
            : type === "boolean"
              ? typeof value === "boolean"
              : type === "number"
                ? typeof value === "number" && Number.isFinite(value)
                : value !== null &&
                  typeof value === "object" &&
                  !Array.isArray(value);
        if (!valid)
          throw new Error(`Input “${name}” does not match type ${type}.`);
      }
      return [name, { type, value }];
    }),
  );
}
