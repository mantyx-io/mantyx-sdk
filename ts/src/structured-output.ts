/** Requested output-schema enforcement policy. Omission preserves best-effort behavior. */
export type OutputSchemaEnforcement = "best_effort" | "strict";

/** Mechanism the platform used to constrain a structured-output run. */
export type StructuredOutputEnforcementMechanism =
  | "native_schema"
  | "synthetic_tool"
  | "none";

/** Terminal observability metadata for a structured-output run. */
export interface StructuredOutputInfo {
  schemaRequested: boolean;
  schemaEnforced: boolean;
  enforcementMechanism: StructuredOutputEnforcementMechanism;
  unconstrainedFallbackOccurred: boolean;
}
