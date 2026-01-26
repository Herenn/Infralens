/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_WS_URL?: string
  readonly VITE_API_URL?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}

// html-to-image types
declare module 'html-to-image' {
  export interface Options {
    backgroundColor?: string
    width?: number
    height?: number
    style?: Partial<CSSStyleDeclaration>
    filter?: (node: HTMLElement) => boolean
    quality?: number
    pixelRatio?: number
  }
  
  export function toPng(node: HTMLElement, options?: Options): Promise<string>
  export function toJpeg(node: HTMLElement, options?: Options): Promise<string>
  export function toSvg(node: HTMLElement, options?: Options): Promise<string>
  export function toBlob(node: HTMLElement, options?: Options): Promise<Blob>
}
