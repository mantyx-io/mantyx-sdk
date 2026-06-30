"""Local tool that returns a file alongside its textual result.

``render_bar_chart`` renders a dependency-free SVG chart in this process and
hands it back to the model as a file via :class:`mantyx.ToolResult`. MANTYX
surfaces the bytes to the model on its next turn as a native file part.

Usage:
    export MANTYX_API_KEY=mtx_live_...
    export MANTYX_WORKSPACE_SLUG=acme-corp
    python main.py
"""

from __future__ import annotations

import base64
import os
import sys
from xml.sax.saxutils import escape

from pydantic import BaseModel

from mantyx import MantyxClient, ToolResult, ToolResultFile, define_local_tool


class ChartArgs(BaseModel):
    title: str
    values: list[float]


def bar_chart_svg(title: str, values: list[float]) -> str:
    """Minimal dependency-free SVG bar chart."""
    bar_w, gap, pad_x, pad_top, chart_h = 48, 16, 24, 48, 180
    max_v = max([*values, 1])
    width = pad_x * 2 + len(values) * bar_w + (len(values) - 1) * gap
    height = pad_top + chart_h + 32
    bars = []
    for i, v in enumerate(values):
        h = round((v / max_v) * chart_h)
        x = pad_x + i * (bar_w + gap)
        y = pad_top + (chart_h - h)
        bars.append(
            f'<rect x="{x}" y="{y}" width="{bar_w}" height="{h}" rx="4" fill="#4f46e5" />'
            f'<text x="{x + bar_w / 2}" y="{y - 6}" font-size="13" '
            f'text-anchor="middle" fill="#111">{v:g}</text>'
        )
    return (
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" '
        f'viewBox="0 0 {width} {height}">'
        '<rect width="100%" height="100%" fill="#fff" />'
        f'<text x="{width / 2}" y="28" font-size="18" font-weight="600" '
        f'text-anchor="middle" fill="#111">{escape(title)}</text>'
        f"{''.join(bars)}"
        "</svg>"
    )


def render_bar_chart(args: ChartArgs) -> ToolResult:
    svg = bar_chart_svg(args.title, args.values)
    return ToolResult(
        result=(
            f'Rendered "{args.title}" as a {len(args.values)}-bar chart '
            f"(max value {max(args.values):g})."
        ),
        files=[
            ToolResultFile(
                filename="chart.svg",
                mime_type="image/svg+xml",
                data=base64.b64encode(svg.encode("utf-8")).decode("ascii"),
            ),
        ],
    )


def required_env(name: str) -> str:
    v = os.environ.get(name)
    if not v:
        print(f"Missing required env var {name}", file=sys.stderr)
        sys.exit(1)
    return v


def main() -> None:
    client = MantyxClient(
        api_key=required_env("MANTYX_API_KEY"),
        workspace_slug=required_env("MANTYX_WORKSPACE_SLUG"),
        base_url=os.environ.get("MANTYX_BASE_URL", "https://app.mantyx.io"),
    )

    tool = define_local_tool(
        name="render_bar_chart",
        description="Render a labelled bar chart from a series of numbers and return it as an SVG image.",
        parameters=ChartArgs,
        execute=render_bar_chart,
    )

    result = client.run_agent(
        system_prompt=(
            "You are a data-viz assistant. When asked for a chart, call "
            "render_bar_chart, then describe the resulting image in one sentence."
        ),
        prompt=(
            'Plot our weekly sales — 12, 19, 8, 15, 22 — titled "Weekly Sales" '
            "and describe the trend."
        ),
        tools=[tool],
        on_assistant_delta=lambda d: print(d, end="", flush=True),
    )
    print()
    print("---")
    print("Final reply:", result.text)


if __name__ == "__main__":
    main()
