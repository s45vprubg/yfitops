/// <reference types="vite/client" />
/// <reference types="vite-plugin-pwa/client" />

interface ImportMetaEnv {
  readonly VITE_WT_URL?: string;
  readonly VITE_HTTP_URL?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
