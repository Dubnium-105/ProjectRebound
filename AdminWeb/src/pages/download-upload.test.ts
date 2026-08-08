import { describe, expect, it, vi } from "vitest";
import { putObject, retry, runConcurrent, type SignedRequest } from "./download-upload";
import { hashBlob } from "../workers/sha256";

describe("download upload helpers", () => {
    it("hashes a browser Blob incrementally and reports completion", async () => {
        const progress: number[] = [];
        const digest = await hashBlob(new Blob(["abc"]), (loaded) => progress.push(loaded));
        expect(digest).toBe("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
        expect(progress).toEqual([3]);
    });

    it("retries at most three times", async () => {
        let attempts = 0;
        const waits: number[] = [];
        await retry(async () => {
            attempts++;
            if (attempts < 3) throw new Error("transient");
        }, 3, async (milliseconds) => { waits.push(milliseconds); });
        expect(attempts).toBe(3);
        expect(waits).toEqual([500, 1000]);
    });

    it("limits multipart work to four concurrent requests", async () => {
        let active = 0;
        let maximum = 0;
        await runConcurrent(Array.from({ length: 12 }, (_, index) => index), 4, async () => {
            active++;
            maximum = Math.max(maximum, active);
            await Promise.resolve();
            active--;
        });
        expect(maximum).toBe(4);
    });

    it("forwards signed headers without forbidden browser headers", async () => {
        const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 200 }));
        vi.stubGlobal("fetch", fetchMock);
        const signed: SignedRequest = {
            url: "https://storage.example/object", method: "PUT", expires_at: "2099-01-01T00:00:00Z",
            headers: { Host: "storage.example", "Content-Length": "3", "Content-Type": "application/zip", "If-None-Match": "*", "x-amz-meta-sha256": "abc" }
        };
        await putObject(signed, new Blob(["abc"]));
        const init = fetchMock.mock.calls[0][1] as RequestInit;
        const headers = init.headers as Headers;
        expect(headers.get("host")).toBeNull();
        expect(headers.get("content-length")).toBeNull();
        expect(headers.get("content-type")).toBe("application/zip");
        expect(headers.get("if-none-match")).toBe("*");
        expect(headers.get("x-amz-meta-sha256")).toBe("abc");
    });

    it("accepts an immutable-object 412 only while resuming a single upload", async () => {
        const signed: SignedRequest = {
            url: "https://storage.example/object", method: "PUT", expires_at: "2099-01-01T00:00:00Z",
            headers: { "If-None-Match": "*" }
        };
        vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(null, { status: 412 })));

        await expect(putObject(signed, new Blob(["abc"]), true)).resolves.toBeUndefined();
        await expect(putObject(signed, new Blob(["abc"]))).rejects.toThrow("HTTP 412");
    });
});
