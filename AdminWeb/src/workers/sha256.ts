import { createSHA256 } from "hash-wasm";

export async function hashBlob(file: Blob, onProgress?: (loaded: number, total: number) => void) {
    const hasher = await createSHA256();
    const chunkSize = 4 * 1024 * 1024;
    let offset = 0;
    while (offset < file.size) {
        const chunk = await file.slice(offset, Math.min(file.size, offset + chunkSize)).arrayBuffer();
        hasher.update(new Uint8Array(chunk));
        offset += chunk.byteLength;
        onProgress?.(offset, file.size);
    }
    return hasher.digest("hex");
}
