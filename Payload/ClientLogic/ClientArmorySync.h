#pragma once

// Keeps the client-side native armory ownership mirrors aligned with the
// complete item table. All functions must be called from the Unreal game
// thread.
void ResetClientArmorySync();
void PumpClientArmorySync();
void PrepareClientArmoryEntry();
