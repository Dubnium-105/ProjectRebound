#pragma once

// Client-side bridge from the authenticated MetaTunnel loadout baseline to
// the game's native FieldMod cache. HTTP work is performed off the game
// thread; Unreal objects are touched only by Pump/Prepare on the game thread.

void ResetClientLoadoutSync();
void PumpClientLoadoutSync();
void PrepareClientLoadoutConsumer(const char* reason);
