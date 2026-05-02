/**
 * Public tool helpers for the MANTYX SDK.
 *
 *   defineLocalTool({ name, description, parameters, execute })
 *     → A tool that runs in the developer's process. The MANTYX server pauses
 *       the agent loop, emits a `local_tool_call` event, and waits for the SDK
 *       to POST the result back.
 *
 *   mantyxTool(id)            → Reference an existing workspace `Tool` row by id.
 *   mantyxPluginTool(name)    → Reference a built-in plugin tool by `@plugin/tool` name.
 */
import type { z } from "zod";

export type ZodLikeObject = z.ZodType<Record<string, unknown>> & {
  _def?: unknown;
  parse?: (value: unknown) => unknown;
};

export interface LocalTool<TArgs = Record<string, unknown>> {
  readonly kind: "local";
  readonly name: string;
  readonly description: string;
  readonly parameters: ZodLikeObject | undefined;
  readonly execute: (args: TArgs) => Promise<string> | string;
}

export interface MantyxToolRef {
  readonly kind: "mantyx";
  readonly id: string;
}

export interface MantyxPluginToolRef {
  readonly kind: "mantyx_plugin";
  readonly name: string;
}

export type ToolRef = MantyxToolRef | MantyxPluginToolRef | LocalTool;

export interface DefineLocalToolOptions<T extends ZodLikeObject | undefined> {
  /** Lowercase alphanumeric + underscore, max 64 chars. */
  name: string;
  description?: string;
  parameters?: T;
  execute: (args: T extends ZodLikeObject ? z.infer<T> : Record<string, unknown>) => Promise<string> | string;
}

export function defineLocalTool<T extends ZodLikeObject | undefined>(
  opts: DefineLocalToolOptions<T>,
): LocalTool {
  if (!/^[a-zA-Z0-9_]{1,64}$/.test(opts.name)) {
    throw new Error(
      `Invalid local tool name ${JSON.stringify(opts.name)}: must match /^[a-zA-Z0-9_]{1,64}$/`,
    );
  }
  return {
    kind: "local",
    name: opts.name,
    description: opts.description ?? "",
    parameters: opts.parameters,
    execute: opts.execute as LocalTool["execute"],
  };
}

export function mantyxTool(id: string): MantyxToolRef {
  if (typeof id !== "string" || id.length === 0) {
    throw new Error("mantyxTool(id): id must be a non-empty string");
  }
  return { kind: "mantyx", id };
}

export function mantyxPluginTool(name: string): MantyxPluginToolRef {
  if (typeof name !== "string" || !name.startsWith("@") || !name.includes("/")) {
    throw new Error(
      `mantyxPluginTool(name): expected "@plugin-slug/tool-name", got ${JSON.stringify(name)}`,
    );
  }
  return { kind: "mantyx_plugin", name };
}

export function isLocalTool(t: ToolRef): t is LocalTool {
  return t.kind === "local";
}
