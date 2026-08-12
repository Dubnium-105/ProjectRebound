/// <reference types="node" />

import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const gameServersSource = readFileSync(join(process.cwd(), "src/pages/GameServersPage.tsx"), "utf8");
const roomsSource = readFileSync(join(process.cwd(), "src/pages/P2PRoomsPage.tsx"), "utf8");

describe("online resource administrative actions", () => {
    it("only offers server deletion for offline, non-banned instances", () => {
        expect(gameServersSource).toContain('permissions.includes("game_servers.delete") && !item.is_banned && item.state === "OFFLINE"');
        expect(gameServersSource).toContain('method: target.operation === "delete" ? "DELETE" : "POST"');
    });

    it("exposes a distinct persistent server ban action", () => {
        expect(gameServersSource).toContain('permissions.includes("game_servers.ban") && !item.is_banned');
        expect(gameServersSource).toContain('operation: "ban"');
        expect(gameServersSource).toContain('target.operation === "ban"');
    });

    it("only offers room deletion after closure", () => {
        expect(roomsSource).toContain('canDelete && item.state === "CLOSED"');
        expect(roomsSource).toContain('method: "DELETE"');
        expect(roomsSource).toContain('authClient.permissions().includes("rooms.delete")');
    });
});
