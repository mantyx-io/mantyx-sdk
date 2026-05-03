---
title: Models
description: Discovering the model catalog and picking a model for a run.
sidebar:
  order: 4
---

```ts
const { models, defaultModelId } = await client.listModels();
console.log(models.map((m) => `${m.id}\t${m.label}`).join("\n"));

await client.runAgent({
  systemPrompt: "...",
  prompt: "Hi!",
  modelId: "platform:cm6abc123",
});
```

```python
catalog = client.list_models()
print("\n".join(f"{m.id}\t{m.label}" for m in catalog.models))

client.run_agent(
    system_prompt="...",
    prompt="Hi!",
    model_id="platform:cm6abc123",
)
```

`modelId` accepts:

- `platform:<offeringId>` — a platform-hosted model offering.
- `provider:<llmProviderId>` — your own BYOK provider's default model.
- `provider:<llmProviderId>:<vendorModelId>` — your provider, override model.
- `<vendorModelId>` — bare vendor id; only resolves when one workspace provider can run it.
- omitted — workspace default.

Invalid `modelId` values return `400 invalid_model` with a candidate list in the body.

## What `listModels()` returns

```jsonc
{
  "models": [
    {
      "id": "platform:cm6abc123",
      "label": "Anthropic Claude Sonnet 4.5 (platform)",
      "provider": "anthropic",
      "vendorModelId": "claude-sonnet-4-5",
      "source": "platform_offering",
      "contextWindowTokens": 200000,
      "pricing": { "inputPer1MUsd": 3.0, "outputPer1MUsd": 15.0, "cacheReadPer1MUsd": 0.3 }
    },
    {
      "id": "provider:cm6def456",
      "label": "OpenAI (workspace BYOK) — gpt-5.5",
      "provider": "openai",
      "vendorModelId": "gpt-5.5",
      "source": "workspace_provider",
      "contextWindowTokens": 200000,
      "pricing": null
    }
  ],
  "defaultModelId": "platform:cm6abc123"
}
```

`pricing` is best-effort: it's only present on platform offerings. For BYOK providers the workspace owner sees the cost on their own provider account.
