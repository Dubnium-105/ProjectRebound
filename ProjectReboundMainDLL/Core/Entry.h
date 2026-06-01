#pragma once

// DLL entry point.
// DllMain() spawns MainThread() on DLL_PROCESS_ATTACH.
// MainThread() owns the full server/client initialization sequence.

void MainThread();
