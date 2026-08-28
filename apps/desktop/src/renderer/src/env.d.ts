/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly DEV: boolean;
  readonly VITE_REACT_GRAB?: string;
  readonly VITE_MULTICA_DESKTOP_VARIANT?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
