import { z } from "zod";
import { MantyxClient, defineLocalTool, type LocalToolResult } from "@mantyx/sdk";

const apiKey = required("MANTYX_API_KEY");
const workspaceSlug = required("MANTYX_WORKSPACE_SLUG");

const client = new MantyxClient({
  apiKey,
  workspaceSlug,
  ...(process.env.MANTYX_BASE_URL ? { baseUrl: process.env.MANTYX_BASE_URL } : {}),
});

// A local tool that renders a bar chart and hands the SVG back to the model
// as a file. The model sees both the textual summary and the rendered image
// on its next turn — the SDK posts the file alongside `result`.
const renderChart = defineLocalTool({
  name: "render_bar_chart",
  description: "Render a labelled bar chart from a series of numbers and return it as an SVG image.",
  parameters: z.object({
    title: z.string().describe("Chart title shown above the bars."),
    values: z.array(z.number()).min(1).describe("The bar heights, left to right."),
  }),
  execute: ({ title, values }): LocalToolResult => {
    const svg = barChartSvg(title, values);
    return {
      result: `Rendered "${title}" as a ${values.length}-bar chart (max value ${Math.max(...values)}).`,
      files: [
        {
          filename: "chart.svg",
          mimeType: "image/svg+xml",
          data: Buffer.from(svg, "utf8").toString("base64"),
        },
      ],
    };
  },
});

async function main(): Promise<void> {
  const result = await client.runAgent({
    systemPrompt:
      "You are a data-viz assistant. When asked for a chart, call render_bar_chart, then describe the resulting image in one sentence.",
    prompt: "Plot our weekly sales — 12, 19, 8, 15, 22 — titled \"Weekly Sales\" and describe the trend.",
    tools: [renderChart],
    onAssistantDelta: (s: string) => process.stdout.write(s),
  });
  process.stdout.write("\n---\n");
  console.log("Final reply:", result.text);
}

// Minimal dependency-free SVG bar chart.
function barChartSvg(title: string, values: number[]): string {
  const barW = 48;
  const gap = 16;
  const padX = 24;
  const padTop = 48;
  const chartH = 180;
  const max = Math.max(...values, 1);
  const width = padX * 2 + values.length * barW + (values.length - 1) * gap;
  const height = padTop + chartH + 32;
  const bars = values
    .map((v, i) => {
      const h = Math.round((v / max) * chartH);
      const x = padX + i * (barW + gap);
      const y = padTop + (chartH - h);
      return (
        `<rect x="${x}" y="${y}" width="${barW}" height="${h}" rx="4" fill="#4f46e5" />` +
        `<text x="${x + barW / 2}" y="${y - 6}" font-size="13" text-anchor="middle" fill="#111">${v}</text>`
      );
    })
    .join("");
  return (
    `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}" viewBox="0 0 ${width} ${height}">` +
    `<rect width="100%" height="100%" fill="#fff" />` +
    `<text x="${width / 2}" y="28" font-size="18" font-weight="600" text-anchor="middle" fill="#111">${escapeXml(title)}</text>` +
    bars +
    `</svg>`
  );
}

function escapeXml(s: string): string {
  return s.replace(/[<>&'"]/g, (c) => {
    switch (c) {
      case "<":
        return "&lt;";
      case ">":
        return "&gt;";
      case "&":
        return "&amp;";
      case "'":
        return "&apos;";
      default:
        return "&quot;";
    }
  });
}

function required(name: string): string {
  const v = process.env[name];
  if (!v) {
    console.error(`Missing required env var ${name}`);
    process.exit(1);
  }
  return v;
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
