/**
 * @mantyx/sdk — TypeScript client for the MANTYX agent runtime.
 *
 * Public surface:
 *
 *   import { MantyxClient, defineLocalTool, mantyxTool, mantyxPluginTool } from "@mantyx/sdk";
 *
 *   // Workspace API key (token prefix `mantyx_`):
 *   const client = new MantyxClient({
 *     apiKey: process.env.MANTYX_API_KEY!,
 *     workspaceSlug: process.env.MANTYX_WORKSPACE_SLUG!,
 *   });
 *
 *   // Or, equivalently, a MANTYX OAuth 2.0 access token
 *   // (token prefix `mantyx_at_`):
 *   const client = new MantyxClient({
 *     accessToken: process.env.MANTYX_ACCESS_TOKEN!,
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
  parseRunOutput,
} from "./client.js";
export type {
  MantyxClientOptions,
  ModelInfo,
  ModelCatalog,
  AgentSpecBase,
  RunSpec,
  SessionSpec,
  OutputSchema,
  LoopDetection,
  ToolBudget,
  ToolBudgets,
  RunResult,
  RunEvent,
  RunEventBase,
  AssistantDeltaEvent,
  ThinkingDeltaEvent,
  AssistantMessageEvent,
  ServerToolResultEvent,
  LocalToolCallEvent,
  LocalToolResultInEvent,
  LoopDetectedEvent,
  ToolBudgetExceededEvent,
  ResultEvent,
  ErrorEvent,
  CancelledEvent,
  SessionInfo,
  LocalHandlers,
} from "./client.js";

export {
  defineLocalTool,
  mantyxTool,
  mantyxPluginTool,
  mantyxA2A,
  defineLocalA2A,
  mantyxMcp,
  defineLocalMcp,
  isLocalTool,
  isLocalA2ATool,
  isLocalMcpServer,
} from "./tools.js";
export type {
  LocalTool,
  MantyxToolRef,
  MantyxPluginToolRef,
  ToolRef,
  ZodLikeObject,
  DefineLocalToolOptions,
  ReasoningLevel,
  A2AToolRef,
  LocalA2ATool,
  McpToolRef,
  LocalMcpServer,
  LocalMcpHttpTransport,
  LocalMcpStdioTransport,
  MantyxA2AOptions,
  DefineLocalA2AOptions,
  MantyxMcpOptions,
  DefineLocalMcpOptions,
} from "./tools.js";

export {
  MantyxError,
  MantyxAuthError,
  MantyxNetworkError,
  MantyxParseError,
  MantyxScopeError,
  MantyxToolError,
  MantyxRunError,
} from "./errors.js";
export type { MantyxRunErrorInit } from "./errors.js";

export {
  MantyxOAuthClient,
  MantyxOAuthError,
  DEFAULT_OAUTH_BASE_URL,
  DEFAULT_REFRESH_SKEW_MS,
  generatePkceVerifier,
  pkceChallenge,
} from "./oauth.js";
export type {
  OAuthToken,
  TokenSource,
  TokenRequestReason,
  MantyxOAuthClientOptions,
  ExchangeAuthorizationCodeOptions,
  RefreshOptions,
  ClientCredentialsOptions,
  RevokeOptions,
  RefreshTokenSourceOptions,
  ClientCredentialsTokenSourceOptions,
} from "./oauth.js";

export { zodToJsonSchema, toToolParametersWire } from "./zod-to-json-schema.js";

export { readSseStream } from "./sse.js";
export type { SseEvent, SseStreamOptions } from "./sse.js";

export { SDK_VERSION } from "./version.js";

// Note: `@mantyx/sdk/a2a-server` is exported as a separate sub-path so apps
// that don't expose an A2A server never load `@a2a-js/sdk` or `express`. Import
// from there to reach `MantyxAgentExecutor`, `serveAgentOverA2A`, and the
// associated types.
