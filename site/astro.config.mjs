import { defineConfig } from "astro/config";
import starlight from "@astrojs/starlight";
import tailwindcss from "@tailwindcss/vite";

const SITE_BASE = process.env.SITE_BASE ?? "/mantyx-sdk";
const SITE_URL = process.env.SITE_URL ?? "https://mantyx-io.github.io";

export default defineConfig({
  site: SITE_URL,
  base: SITE_BASE,
  trailingSlash: "always",
  vite: {
    plugins: [tailwindcss()],
  },
  integrations: [
    starlight({
      title: "MANTYX SDK",
      description:
        "Official client SDKs for the MANTYX agent runtime. TypeScript, Go, and Python.",
      favicon: "/favicon.png",
      logo: {
        src: "./src/assets/mantyx-mark.png",
        alt: "",
      },
      components: {
        Hero: "./src/components/starlight/EmptyHero.astro",
        SiteTitle: "./src/components/starlight/SiteTitle.astro",
        Header: "./src/components/starlight/Header.astro",
      },
      social: [
        {
          icon: "github",
          label: "GitHub",
          href: "https://github.com/mantyx-io/mantyx-sdk",
        },
      ],
      customCss: ["./src/styles/global.css"],
      editLink: {
        baseUrl: "https://github.com/mantyx-io/mantyx-sdk/edit/main/site/",
      },
      lastUpdated: true,
      head: [
        {
          tag: "meta",
          attrs: {
            name: "theme-color",
            content: "#0a0a0a",
          },
        },
      ],
      sidebar: [
        {
          label: "Getting started",
          items: [
            { label: "Overview", link: "/docs/getting-started/" },
            { label: "Authentication", link: "/docs/getting-started/authentication/" },
            { label: "Quickstart", link: "/docs/quickstart/" },
          ],
        },
        {
          label: "Agents",
          items: [
            { label: "Ephemeral agents", link: "/docs/agents/ephemeral/" },
            { label: "Persisted agents (agentId)", link: "/docs/agents/persisted/" },
            { label: "Sessions", link: "/docs/agents/sessions/" },
          ],
        },
        {
          label: "Tools",
          items: [
            { label: "Local tools", link: "/docs/tools/local/" },
            { label: "MANTYX tools", link: "/docs/tools/mantyx/" },
            { label: "Plugin tools", link: "/docs/tools/plugin/" },
            { label: "Agent2Agent (A2A)", link: "/docs/tools/a2a/" },
            { label: "MCP connectors", link: "/docs/tools/mcp/" },
          ],
        },
        {
          label: "Concepts",
          items: [
            { label: "Models", link: "/docs/models/" },
            { label: "Reasoning level", link: "/docs/reasoning/" },
            { label: "Output schema", link: "/docs/output-schema/" },
            { label: "Streaming", link: "/docs/streaming/" },
            { label: "Errors", link: "/docs/errors/" },
            { label: "Metadata", link: "/docs/metadata/" },
          ],
        },
        {
          label: "Protocol",
          items: [
            { label: "Agent-runs protocol", link: "/docs/protocol/" },
            { label: "Wire protocol — messaging", link: "/docs/wire-protocol/" },
          ],
        },
        {
          label: "Reference",
          items: [
            { label: "TypeScript (@mantyx/sdk)", link: "/docs/reference/typescript/" },
            { label: "Go (mantyx-go-sdk)", link: "/docs/reference/go/" },
            { label: "Python (mantyx-sdk)", link: "/docs/reference/python/" },
          ],
        },
        { label: "Examples", link: "/docs/examples/" },
      ],
    }),
  ],
});
