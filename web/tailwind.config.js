/** Design tokens are CSS variables, light and dark (ADR-0013). */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  darkMode: "media",
  theme: {
    extend: {
      colors: {
        background: "var(--background)",
        foreground: "var(--foreground)",
        muted: "var(--muted)",
        border: "var(--border)",
        accent: "var(--accent)",
        danger: "var(--danger)",
        ok: "var(--ok)",
      },
    },
  },
  plugins: [],
};
