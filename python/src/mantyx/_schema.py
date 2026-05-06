"""Pydantic → JSON Schema conversion for local-tool parameter definitions.

The MANTYX server feeds the resulting schema to LLM providers verbatim,
so we map Pydantic v2 models (the recommended way) and pass through
already-shaped JSON Schema dicts. Anything else is rejected with a
:class:`TypeError`.
"""

from __future__ import annotations

from typing import Any

from pydantic import BaseModel

JsonSchema = dict[str, Any]
ParametersInput = type[BaseModel] | JsonSchema | None


def _strip_pydantic_metadata(schema: JsonSchema) -> JsonSchema:
    """Remove the ``$defs`` / ``title`` artefacts Pydantic emits when the
    consumer is happy with an inline JSON Schema."""
    cleaned = dict(schema)
    cleaned.pop("title", None)
    return cleaned


def to_tool_parameters_wire(parameters: ParametersInput) -> JsonSchema:
    """Coerce a parameters spec into a JSON Schema suitable for the wire.

    Accepts:
      - A Pydantic v2 ``BaseModel`` subclass (the recommended way).
      - An already-shaped JSON Schema ``dict``.
      - ``None`` — defaults to a permissive ``{"type": "object", "properties": {}}``.
    """
    if parameters is None:
        return {"type": "object", "properties": {}}

    if isinstance(parameters, dict):
        return dict(parameters)

    if isinstance(parameters, type) and issubclass(parameters, BaseModel):
        schema = parameters.model_json_schema()
        return _strip_pydantic_metadata(schema)

    raise TypeError(
        "parameters must be a Pydantic BaseModel subclass, a JSON Schema dict, or None; "
        f"got {type(parameters).__name__}"
    )


def parse_args_with_pydantic(parameters: ParametersInput, raw: dict[str, Any] | None) -> Any:
    """If `parameters` is a Pydantic model, validate `raw` against it and
    return the model instance; otherwise pass `raw` through unchanged."""
    if isinstance(parameters, type) and issubclass(parameters, BaseModel):
        return parameters.model_validate(raw or {})
    return raw or {}


__all__ = [
    "JsonSchema",
    "ParametersInput",
    "parse_args_with_pydantic",
    "to_tool_parameters_wire",
]
