/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_WT_URL?: string;
  readonly VITE_HTTP_URL?: string;
  readonly VITE_JOIN_URL?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
