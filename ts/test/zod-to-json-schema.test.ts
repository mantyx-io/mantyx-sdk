import { describe, expect, it } from "vitest";
import { z } from "zod";
import { zodToJsonSchema, toToolParametersWire } from "../src/zod-to-json-schema.js";

describe("zodToJsonSchema", () => {
  it("converts a Zod v3 object with required number fields", () => {
    const schema = z.object({ a: z.number(), b: z.number() });
    expect(zodToJsonSchema(schema)).toEqual({
      type: "object",
      properties: {
        a: { type: "number" },
        b: { type: "number" },
      },
      required: ["a", "b"],
    });
  });

  it("marks optional and default fields as not required", () => {
    const schema = z.object({
      required: z.string(),
      optional: z.string().optional(),
      withDefault: z.string().default("x"),
    });
    const out = zodToJsonSchema(schema);
    expect(out.required).toEqual(["required"]);
  });

  it("uses schema.toJSONSchema() when present (Zod v4 cross-instance path)", () => {
    const schema = z.object({ a: z.number(), b: z.number() });
    const fakeJsonSchema = {
      type: "object",
      properties: { a: { type: "number" }, b: { type: "number" } },
      required: ["a", "b"],
    };
    const withInstanceMethod = Object.assign(schema, {
      toJSONSchema: () => fakeJsonSchema,
    });
    expect(zodToJsonSchema(withInstanceMethod)).toEqual(fakeJsonSchema);
  });

  it("falls back to manual Zod v4 _def.type walker when instance toJSONSchema is absent", () => {
    const fieldA = { _def: { type: "number" } } as unknown as z.ZodType;
    const fieldB = { _def: { type: "number" } } as unknown as z.ZodType;
    const fakeV4Object = {
      _def: {
        type: "object",
        shape: { a: fieldA, b: fieldB },
      },
    } as unknown as z.ZodType;
    expect(zodToJsonSchema(fakeV4Object)).toEqual({
      type: "object",
      properties: {
        a: { type: "number" },
        b: { type: "number" },
      },
      required: ["a", "b"],
    });
  });

  it("does not treat a JSON Schema dict as a Zod schema", () => {
    const jsonSchema = {
      type: "object",
      properties: { x: { type: "string" } },
    };
    expect(toToolParametersWire(jsonSchema)).toBe(jsonSchema);
  });
});
