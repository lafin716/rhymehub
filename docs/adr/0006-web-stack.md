# ADR 0006 — React + Vite + Tailwind PWA embedded in the Go binary
Status: accepted
Decision: React 19, TypeScript, Vite, Tailwind v4 with hand-written shadcn-style primitives (no CLI codegen),
manual service worker for Web Push. Build output embedded via go:embed so one binary serves UI + API.
