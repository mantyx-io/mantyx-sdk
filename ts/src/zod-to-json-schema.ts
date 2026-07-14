/**
 * Lightweight Zod → JSON Schema converter for tool parameter definitions.
 *
 * Resolution order:
 *   1. `schema.toJSONSchema()` on the instance (Zod v4; works across package
 *      copies when the user's `zod` and the SDK's `zod` are different installs)
 *   2. `z.toJSONSchema(schema)` from the SDK's `zod` import (Zod v4, same copy)
 *   3. Hand-rolled walker for Zod v3 `typeName` and Zod v4 `_def.type`
 *
 * The output is a JSON-Schema-shaped object with `type: "object"`, `properties`,
 * and `required`. The MANTYX server feeds this to LLM providers verbatim, so
 * unsupported zod features (effects, transforms, intersections) degrade to a
 * permissive `"object"` description rather than failing.
 */
import { z } from "zod";

type JsonSchema = Record<string, unknown>;

type ZodDef = {
  typeName?: string;
  type?: string;
  value?: unknown;
  values?: readonly string[];
  innerType?: z.ZodType<unknown>;
  element?: z.ZodType<unknown>;
  shape?: (() => Record<string, z.ZodType<unknown>>) | Record<string, z.ZodType<unknown>>;
};

interface ZodLikeWithToJsonSchema {
  toJSONSchema?: (schema: unknown) => JsonSchema;
}

interface ZodSchemaInstance {
  toJSONSchema?: () => JsonSchema;
  _def?: ZodDef;
}

function isZodSchema(value: unknown): value is z.ZodType<unknown> {
  if (!value || typeof value !== "object") return false;
  const o = value as { _def?: unknown; _zod?: unknown };
  return typeof o._def !== "undefined" || typeof o._zod !== "undefined";
}

function normalizeWireJsonSchema(out: JsonSchema, schema: z.ZodType<unknown>): JsonSchema {
  const normalized = { ...out };
  delete normalized.$schema;
  delete normalized.additionalProperties;

  const def = (schema as ZodSchemaInstance)._def;
  const kind = schemaKind(def);
  if (kind === "ZodObject" || kind === "object") {
    const shape = def?.shape;
    const fields =
      typeof shape === "function" ? shape() : (shape as Record<string, z.ZodType<unknown>> | undefined);
    if (fields) {
      const required: string[] = [];
      for (const [key, value] of Object.entries(fields)) {
        if (!isOptionalField(value)) {
          required.push(key);
        }
      }
      if (required.length > 0) normalized.required = required;
      else delete normalized.required;
    }
  }

  return normalized;
}

export function zodToJsonSchema(schema: z.ZodType<unknown>): JsonSchema {
  const instance = schema as unknown as ZodSchemaInstance;
  if (typeof instance.toJSONSchema === "function") {
    try {
      const out = instance.toJSONSchema();
      if (out && typeof out === "object") return normalizeWireJsonSchema(out, schema);
    } catch {
      // fall through
    }
  }

  const builtIn = (z as unknown as ZodLikeWithToJsonSchema).toJSONSchema;
  if (typeof builtIn === "function") {
    try {
      const out = builtIn.call(z, schema) as JsonSchema;
      if (out && typeof out === "object") return normalizeWireJsonSchema(out, schema);
    } catch {
      // fall through to manual converter
    }
  }
  return convertNode(schema);
}

function schemaKind(def: ZodDef | undefined): string | undefined {
  if (!def) return undefined;
  if (typeof def.typeName === "string") return def.typeName;
  if (typeof def.type === "string") return def.type;
  return undefined;
}

function isOptionalField(field: z.ZodType<unknown>): boolean {
  const kind = schemaKind((field as ZodSchemaInstance)._def);
  return (
    kind === "ZodOptional" ||
    kind === "ZodDefault" ||
    kind === "optional" ||
    kind === "default"
  );
}

function convertNode(schema: z.ZodType<unknown>): JsonSchema {
  const def = (schema as ZodSchemaInstance)._def;
  const kind = schemaKind(def);
  switch (kind) {
    case "ZodString":
    case "string":
      return { type: "string" };
    case "ZodNumber":
    case "number":
      return { type: "number" };
    case "ZodBoolean":
    case "boolean":
      return { type: "boolean" };
    case "ZodNull":
    case "null":
      return { type: "null" };
    case "ZodLiteral":
    case "literal": {
      const value = def?.value;
      return { const: value, type: typeof value };
    }
    case "ZodEnum":
    case "enum": {
      const values = def?.values ?? [];
      return { type: "string", enum: [...values] };
    }
    case "ZodArray": {
      const inner = (def as { type?: z.ZodType<unknown> }).type;
      return {
        type: "array",
        items: inner ? convertNode(inner) : {},
      };
    }
    case "array": {
      const inner = def?.element;
      return {
        type: "array",
        items: inner ? convertNode(inner) : {},
      };
    }
    case "ZodOptional":
    case "ZodNullable":
    case "optional":
    case "nullable": {
      const inner = def?.innerType;
      return inner ? convertNode(inner) : {};
    }
    case "ZodDefault":
    case "default": {
      const inner = def?.innerType;
      return inner ? convertNode(inner) : {};
    }
    case "ZodObject":
    case "object": {
      const shape = def?.shape;
      const fields =
        typeof shape === "function" ? shape() : (shape as Record<string, z.ZodType<unknown>> | undefined);
      const properties: Record<string, JsonSchema> = {};
      const required: string[] = [];
      if (fields) {
        for (const [key, value] of Object.entries(fields)) {
          properties[key] = convertNode(value);
          if (!isOptionalField(value)) {
            required.push(key);
          }
        }
      }
      const out: JsonSchema = { type: "object", properties };
      if (required.length > 0) out.required = required;
      return out;
    }
    default:
      return {};
  }
}

/**
 * Coerce a JSON-Schema-shaped value into a wire object suitable for the
 * MANTYX local-tool definition payload. Accepts either a Zod schema or an
 * already-shaped JSON Schema object.
 */
export function toToolParametersWire(
  parameters: z.ZodType<unknown> | JsonSchema | undefined,
): JsonSchema {
  if (!parameters) return { type: "object", properties: {} };
  if (isZodSchema(parameters)) {
    return zodToJsonSchema(parameters);
  }
  return parameters as JsonSchema;
}
