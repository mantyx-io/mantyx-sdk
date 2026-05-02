/**
 * @mantyx/sdk — TypeScript client for the MANTYX agent runtime.
 *
 * Public surface:
 *
 *   import { MantyxClient, defineLocalTool, mantyxTool, mantyxPluginTool } from "@mantyx/sdk";
 *
 *   const client = new MantyxClient({
 *     apiKey: process.env.MANTYX_API_KEY!,
 *     workspaceSlug: process.env.MANTYX_WORKSPACE_SLUG!,
 *   });
 *
 *   const result = await client.runAgent({
 *     systemPrompt: "You are a helpful assistant.",
 *     prompt: "Read /etc/hostname and tell me what it says.",
 *     tools: [
 *       defineLocalTool({
 *         name: "read_file",
 *         description: "Read a file from the local filesystem.",
 *         parameters: z.object({ path: z.string() }),
 *         execute: async ({ path }) => fs.readFile(path, "utf8"),
 *       }),
 *     ],
 *   });
 *
 *   console.log(result.text);
 */

export {
  MantyxClient,
  AgentSession,
  DEFAULT_BASE_URL,
} from "./client.js";
export type {
  MantyxClientOptions,
  ModelInfo,
  ModelCatalog,
  AgentSpecBase,
  RunSpec,
  SessionSpec,
  RunResult,
  RunEvent,
  RunEventBase,
  AssistantDeltaEvent,
  ThinkingDeltaEvent,
  AssistantMessageEvent,
  ServerToolResultEvent,
  LocalToolCallEvent,
  LocalToolResultInEvent,
  ResultEvent,
  ErrorEvent,
  CancelledEvent,
  SessionInfo,
} from "./client.js";

export {
  defineLocalTool,
  mantyxTool,
  mantyxPluginTool,
  isLocalTool,
} from "./tools.js";
export type {
  LocalTool,
  MantyxToolRef,
  MantyxPluginToolRef,
  ToolRef,
  ZodLikeObject,
  DefineLocalToolOptions,
} from "./tools.js";

export {
  MantyxError,
  MantyxAuthError,
  MantyxNetworkError,
  MantyxToolError,
  MantyxRunError,
} from "./errors.js";

export { zodToJsonSchema, toToolParametersWire } from "./zod-to-json-schema.js";

export { readSseStream } from "./sse.js";
export type { SseEvent, SseStreamOptions } from "./sse.js";

export { SDK_VERSION } from "./version.js";
