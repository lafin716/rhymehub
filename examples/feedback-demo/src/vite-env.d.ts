/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_RHYMEHUB_PROJECT?: string;
  readonly VITE_RHYMEHUB_KEY?: string;
  readonly VITE_RHYMEHUB_ENDPOINT?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
