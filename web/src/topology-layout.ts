import ELK from "elkjs/lib/elk-api";
import workerURL from "elkjs/lib/elk-worker.min.js?url";

// Bundled same-origin classic Web Worker, never an external service or CDN.
export function createLayoutEngine() {
  return new ELK({ workerUrl: workerURL });
}
