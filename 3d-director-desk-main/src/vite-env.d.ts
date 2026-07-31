/// <reference types="vite/client" />

declare const __LOCAL_GUO_ASSETS_AVAILABLE__: boolean;
declare const __LOCAL_MIXAMO_CHARACTER_AVAILABLE__: boolean;
declare const __LOCAL_MIXAMO_ANIMATIONS_AVAILABLE__: boolean;
declare const __APP_VERSION__: string;
declare const __CORNFIELD_EMBEDDED_BUILD__: boolean;

declare module "node:fs" {
  export function existsSync(path: string | URL): boolean;
  export function readFileSync(path: string | URL, encoding: string): string;
}
