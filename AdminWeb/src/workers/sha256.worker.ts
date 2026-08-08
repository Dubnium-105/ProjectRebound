import { hashBlob } from "./sha256";

type HashRequest = { file: File };

self.onmessage = async (event: MessageEvent<HashRequest>) => {
    try {
        const file = event.data.file;
        const sha256 = await hashBlob(file, (loaded, total) => self.postMessage({ type: "progress", loaded, total }));
        self.postMessage({ type: "complete", sha256 });
    }
    catch (error) {
        self.postMessage({
            type: "error",
            message: error instanceof Error ? error.message : "SHA-256 failed"
        });
    }
};

export {};
