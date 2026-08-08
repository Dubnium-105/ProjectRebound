import { hashBlob } from "./sha256";

type HashRequest = { file: File };

// This value is included in every response so CSP changes can deliberately
// produce a new content-hashed worker URL instead of reusing cached headers.
const workerVersion = "wasm-csp-v1";

self.onmessage = async (event: MessageEvent<HashRequest>) => {
    try {
        const file = event.data.file;
        const sha256 = await hashBlob(file, (loaded, total) => self.postMessage({
            type: "progress",
            loaded,
            total,
            workerVersion
        }));
        self.postMessage({ type: "complete", sha256, workerVersion });
    }
    catch (error) {
        self.postMessage({
            type: "error",
            message: error instanceof Error ? error.message : "SHA-256 failed",
            workerVersion
        });
    }
};

export {};
