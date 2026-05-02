/**
 * Lightweight Zod → JSON Schema converter for tool parameter definitions.
 *
 * Tries `z.toJSONSchema` (Zod v4+) first; falls back to a hand-rolled walker
 * for v3 schemas so the SDK works on a wide range of zod versions.
 *
 * The output is a JSON-Schema-shaped object with `type: "object"`, `properties`,
 * and `required`. The MANTYX server feeds this to LLM providers verbatim, so
 * unsupported zod features (effects, transforms, intersections) degrade to a
 * permissive `"object"` description rather than failing.
 */
import { z } from "zod";

type JsonSchema = Record<string, unknown>;

interface ZodLikeWithToJsonSchema {
  toJSONSchema?: (schema: unknown) => JsonSchema;
}

export function zodToJsonSchema(schema: z.ZodType<unknown>): JsonSchema {
  const builtIn = (z as unknown as ZodLikeWithToJsonSchema).toJSONSchema;
  if (typeof builtIn === "function") {
    try {
      const out = builtIn.call(z, schema) as JsonSchema;
      if (out && typeof out === "object") return out;
    } catch {
      // fall through to manual converter
    }
  }
  return convertNode(schema);
}

function convertNode(schema: z.ZodType<unknown>): JsonSchema {
  const def = (schema as unknown as { _def?: { typeName?: string } })._def;
  const typeName = def?.typeName;
  switch (typeName) {
    case "ZodString":
      return { type: "string" };
    case "ZodNumber":
      return { type: "number" };
    case "ZodBoolean":
      return { type: "boolean" };
    case "ZodNull":
      return { type: "null" };
    case "ZodLiteral": {
      const value = (def as { value?: unknown }).value;
      return { const: value, type: typeof value };
    }
    case "ZodEnum": {
      const values = (def as { values?: readonly string[] }).values ?? [];
      return { type: "string", enum: [...values] };
    }
    case "ZodArray": {
      const inner = (def as { type?: z.ZodType<unknown> }).type;
      return {
        type: "array",
        items: inner ? convertNode(inner) : {},
      };
    }
    case "ZodOptional":
    case "ZodNullable": {
      const inner = (def as { innerType?: z.ZodType<unknown> }).innerType;
      return inner ? convertNode(inner) : {};
    }
    case "ZodDefault": {
      const inner = (def as { innerType?: z.ZodType<unknown> }).innerType;
      return inner ? convertNode(inner) : {};
    }
    case "ZodObject": {
      const shape = (def as { shape?: () => Record<string, z.ZodType<unknown>> }).shape;
      const fields = typeof shape === "function" ? shape() : (shape as Record<string, z.ZodType<unknown>> | undefined);
      const properties: Record<string, JsonSchema> = {};
      const required: string[] = [];
      if (fields) {
        for (const [key, value] of Object.entries(fields)) {
          properties[key] = convertNode(value);
          const innerDef = (value as unknown as { _def?: { typeName?: string } })._def;
          const innerTypeName = innerDef?.typeName;
          if (innerTypeName !== "ZodOptional" && innerTypeName !== "ZodDefault") {
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
  if (typeof (parameters as { _def?: unknown })._def !== "undefined") {
    return zodToJsonSchema(parameters as z.ZodType<unknown>);
  }
  return parameters as JsonSchema;
}
